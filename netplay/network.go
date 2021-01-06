package netplay

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"time"
	"hash/crc32"

	"github.com/libretro/ludo/input"
	"github.com/libretro/ludo/state"
)

type SavestatePacket struct {
	total uint32
	index uint32
	tick  int64
	save []byte
}

const inputDelayFrames = 3
const inputHistorySize = int64(300)
const sendHistorySize = 5
const packetSizeLimit = 60 * 1024 //60 kB

// Network code indicating the type of message.
const (
	MsgCodeHandshake    = byte(1) // Used when sending the hand shake.
	MsgCodePlayerInput  = byte(2) // Sends part of the player's input buffer.
	MsgCodePing         = byte(3) // Used to tracking packet round trip time. Expect a "Pong" back.
	MsgCodePong         = byte(4) // Sent in reply to a Ping message for testing round trip time.
	MsgCodeSync         = byte(5) // Used to pass sync data
	MsgCodeSavestate    = byte(6) // Used to load a savestate when a new player arrives
	MsgCodeSavestateReq = byte(7) // Used to request a savestate
)

// Listen is used by the netplay host, listening address and port
var Listen bool

// Join is used by the netplay guest, address of the host
var Join bool

// Conn is the connection between two players
var Conn *net.UDPConn

var enabled = false
var connectedToClient = false
var isServer = false
var confirmedTick = int64(0)
var localSyncData = uint32(0)
var remoteSyncData = uint32(0)
var isStateDesynced = false
var localSyncDataTick = int64(-1)
var remoteSyncDataTick = int64(-1)
var localTickDelta = int64(0)
var remoteTickDelta = int64(0)
var inputHistory = [inputHistorySize]uint32{}
var remoteInputHistory = [inputHistorySize]uint32{}
var clientAddr net.Addr
var latency int64
var lastSyncedTick = int64(-1)
var messages chan []byte
var remoteSavestate []SavestatePacket
var lastTimeSaveRecv = int64(0)

// Init initialises a netplay session between two players
func Init() {
	if Listen { // Host mode
		var err error
		Conn, err = net.ListenUDP("udp", &net.UDPAddr{
			IP:   net.ParseIP("0.0.0.0"),
			Port: 1234,
		})
		if err != nil {
			log.Println("Netplay", err.Error())
			return
		}

		Conn.SetReadBuffer(1048576)

		enabled = true
		isServer = true

		input.InitializeBuffer(0)
		input.InitializeBuffer(1)
		input.LocalPlayerPort = 0
		input.RemotePlayerPort = 1

		log.Println("Netplay", "Listening.")
		go func() {
			buffer := make([]byte, packetSizeLimit)
			_, addr, _ := Conn.ReadFrom(buffer)
	
			connectedToClient = true
			clientAddr = addr

			log.Println("Netplay", "A new peer has arrived")
	
			sendPacket(makeHandshakePacket(), 5)
	
			messages = make(chan []byte, 256)
		}()
		go listen()
		shouldUpdate = true
	} else if Join { // Guest mode
		var err error
		Conn, err = net.ListenUDP("udp", &net.UDPAddr{
			IP:   net.ParseIP("0.0.0.0"),
			Port: 1235,
		})
		if err != nil {
			log.Println("Netplay", err.Error())
			return
		}

		clientAddr = &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 1234,
		}

		Conn.SetReadBuffer(1048576)

		enabled = true
		isServer = false

		input.InitializeBuffer(0)
		input.InitializeBuffer(1)
		input.LocalPlayerPort = 1
		input.RemotePlayerPort = 0

		log.Println("sending handshake")
		sendPacket(makeHandshakePacket(), 5)
		sendPacket(makeSavestateReqPackets(), 1)
		lastTimeSaveRecv = currentTimestamp()

		buffer := make([]byte, packetSizeLimit)
		_, addr, _ := Conn.ReadFrom(buffer)

		connectedToClient = true
		clientAddr = addr
		log.Println("Netplay", "Connected")

		messages = make(chan []byte, 256)
		go listen()
	}
}

// Get input from the remote player for the passed in game tick.
func getRemoteInputState(tick int64) input.PlayerState {
	if tick > confirmedTick {
		// Repeat the last confirmed input when we don't have a confirmed tick
		tick = confirmedTick
		// TODO: uncomment
		// log.Println("Predict:", confirmedTick, remoteInputHistory[(inputHistorySize+tick)%inputHistorySize])
	}
	return decodeInput(remoteInputHistory[(inputHistorySize+tick)%inputHistorySize])
}

// Get input state for the local client
func getLocalInputState(tick int64) input.PlayerState {
	return decodeInput(inputHistory[(inputHistorySize+tick)%inputHistorySize])
}

// Send the inputState for the local player to the remote player for the given game tick.
func sendInputData(tick int64) {
	// Don't send input data when not connect to another player's game client.
	if !(enabled && connectedToClient) {
		return
	}

	//log.Println("Send input packet", tick)

	sendPacket(makeInputPacket(tick), 1)
}

func setLocalInput(st input.PlayerState, tick int64) {
	encodedInput := encodeInput(st)
	inputHistory[(inputHistorySize+tick)%inputHistorySize] = encodedInput
}

func setRemoteEncodedInput(encodedInput uint32, tick int64) {
	remoteInputHistory[(inputHistorySize+tick)%inputHistorySize] = encodedInput
}

// Handles sending packets to the other client. Set duplicates to something > 0 to send more than once.
func sendPacket(packet []byte, duplicates int) {
	if duplicates == 0 {
		duplicates = 1
	}

	for i := 0; i < duplicates; i++ {
		sendPacketRaw(packet)
	}
}

// Send a packet immediately
func sendPacketRaw(packet []byte) {
	_, err := Conn.WriteTo(packet, clientAddr)
	if err != nil {
		log.Println(err)
	}
}

func listen() {
	for {
		buffer := make([]byte, packetSizeLimit)
		n, err := Conn.Read(buffer)
		if err != nil {
			log.Println(err)
			continue
		}
		messages <- buffer[:n]
	}
	//TODO: handle when a player leaves the game
}

// Checks the queue for any incoming packets and process them.
func receiveData() {
	if !enabled {
		return
	}

	// For now we'll process all packets every frame.
	for {
		select {
		case data := <-messages:
			r := bytes.NewReader(data)
			var code byte
			binary.Read(r, binary.LittleEndian, &code)

			// We ask for the savestate every 500ms if we didn't receive it correctly
			if code != MsgCodeSavestate && lastTimeSaveRecv != 0 && currentTimestamp() - lastTimeSaveRecv >= 500 {
				log.Println("Netplay", "Didn't receive the savestate: requesting...")
				sendPacket(makeSavestateReqPackets(), 1)
				lastTimeSaveRecv = currentTimestamp()
				remoteSavestate = nil				
			}

			if code == MsgCodePlayerInput {
				// Break apart the packet into its parts.
				var tickDelta, receivedTick int64
				binary.Read(r, binary.LittleEndian, &tickDelta)
				binary.Read(r, binary.LittleEndian, &receivedTick)

				// We only care about the latest tick delta, so make sure the confirmed frame is atleast the same or newer.
				// This would work better if we added a packet count.
				if receivedTick >= confirmedTick {
					remoteTickDelta = tickDelta
				}

				if receivedTick > confirmedTick {
					if receivedTick-confirmedTick > inputDelayFrames {
						log.Println("Received packet with a tick too far ahead. Last: ", confirmedTick, "     Current: ", receivedTick)
					}

					confirmedTick = receivedTick

					// log.Println("----")
					// log.Println(confirmedTick)
					for offset := int64(sendHistorySize - 1); offset >= 0; offset-- {
						var encodedInput uint32
						binary.Read(r, binary.LittleEndian, &encodedInput)
						// Save the input history sent in the packet.
						setRemoteEncodedInput(encodedInput, receivedTick-offset)
						// log.Println(encodedInput, receivedTick-offset, offset)
					}
				}

				// NetLog("Received Tick: " .. receivedTick .. ",  Input: " .. remoteInputHistory[(confirmedTick % inputHistorySize)+1])
			} else if code == MsgCodePing {
				var pingTime int64
				binary.Read(r, binary.LittleEndian, &pingTime)
				sendPacket(makePongPacket(time.Unix(pingTime, 0)), 1)
			} else if code == MsgCodePong {
				var pongTime int64
				binary.Read(r, binary.LittleEndian, &pongTime)
				latency = time.Now().Unix() - pongTime
				//print("Got pong message: " .. latency)
			} else if code == MsgCodeSync {
				var tick int64
				var syncData uint32
				binary.Read(r, binary.LittleEndian, &tick)
				binary.Read(r, binary.LittleEndian, &syncData)
				// Ignore any tick that isn't more recent than the last sync data
				if !isStateDesynced && tick > remoteSyncDataTick {
					remoteSyncDataTick = tick
					remoteSyncData = syncData

					// Check for a desync
					isDesynced()
				}
			} else if code == MsgCodeSavestate {
				var savePacket SavestatePacket
				binary.Read(r, binary.LittleEndian, &savePacket.total)
				binary.Read(r, binary.LittleEndian, &savePacket.index)
				binary.Read(r, binary.LittleEndian, &savePacket.tick)
				savePacket.save = data[17:] // TODO: find a better way
				remoteSavestate = append(remoteSavestate, savePacket)
				lastTimeSaveRecv = currentTimestamp()

				if len(remoteSavestate) == int(savePacket.total) {
					savestate, tick := joinSave(remoteSavestate)
					binary.Read(r, binary.LittleEndian, savestate)
					s := state.Global.Core.SerializeSize()
					err := state.Global.Core.Unserialize(savestate, s)
					if err != nil {
						log.Println(err)
					} else {
						shouldUpdate = true
						state.Global.Tick = tick
						localSyncDataTick = tick
						remoteSyncDataTick = tick
						confirmedTick = tick
						lastSyncedTick = tick
						remoteSyncData = crc32.ChecksumIEEE(savestate)
						localSyncData = remoteSyncData
					}
					remoteSavestate = nil
					lastTimeSaveRecv = 0
				}
			} else if code == MsgCodeSavestateReq {
				log.Println("Netplay", "Recv savestate request")
				// We send the savestate if we receive a request
				sendSavestate()
			}
		default:
			return
		}
	}
}

// Generate a packet containing information about player input.
func makeInputPacket(tick int64) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodePlayerInput)
	binary.Write(buf, binary.LittleEndian, localTickDelta)
	binary.Write(buf, binary.LittleEndian, tick)

	historyIndexStart := tick - sendHistorySize + 1
	// log.Println("Make input", tick, historyIndexStart)
	for i := int64(0); i < sendHistorySize; i++ {
		encodedInput := inputHistory[(inputHistorySize+historyIndexStart+i)%inputHistorySize]
		binary.Write(buf, binary.LittleEndian, encodedInput)
		// log.Println((inputHistorySize + historyIndexStart + i) % inputHistorySize)
	}

	return buf.Bytes()
}

// Make and send the savestate message
func sendSavestate() {
	savestatePackets := makeSavestatePackets()
	localSyncDataTick = state.Global.Tick
	remoteSyncDataTick = state.Global.Tick
	confirmedTick = state.Global.Tick
	lastSyncedTick = state.Global.Tick
	for i := 0; i < len(savestatePackets); i++ {
		sendPacket(savestatePackets[i], 1)
	}
}

// Send a ping message in order to test network latency
func sendPingMessage() {
	sendPacket(makePingPacket(time.Now()), 1)
}

// Make a ping packet
func makePingPacket(t time.Time) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodePing)
	binary.Write(buf, binary.LittleEndian, t.Unix())
	return buf.Bytes()
}

// Make pong packet
func makePongPacket(t time.Time) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodePong)
	binary.Write(buf, binary.LittleEndian, t.Unix())
	return buf.Bytes()
}

// Sends sync data
func sendSyncData() {
	sendPacket(makeSyncDataPacket(localSyncDataTick, localSyncData), 5)
}

// Make a sync data packet
func makeSyncDataPacket(tick int64, syncData uint32) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeSync)
	binary.Write(buf, binary.LittleEndian, tick)
	binary.Write(buf, binary.LittleEndian, syncData)
	return buf.Bytes()
}

// Generate handshake packet for connecting with another client.
func makeHandshakePacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeHandshake)
	return buf.Bytes()
}

// Make the savestate packets
func makeSavestatePackets() [][]byte {
	s := state.Global.Core.SerializeSize()
	savestate, err := state.Global.Core.Serialize(s)
	if err != nil {
		log.Println(err)
		return nil
	}
	remoteSyncData = crc32.ChecksumIEEE(savestate)
	localSyncData = remoteSyncData
	saves := split(savestate, packetSizeLimit - 17)
	var buffers [][]byte
	for i := 0; i < len(saves); i++ {
		buf := new(bytes.Buffer)
		binary.Write(buf, binary.LittleEndian, MsgCodeSavestate)
		binary.Write(buf, binary.LittleEndian, uint32(len(saves))) // total packets
		binary.Write(buf, binary.LittleEndian, uint32(i+1)) // index
		binary.Write(buf, binary.LittleEndian, int64(state.Global.Tick)) // tick
		buffer := append(buf.Bytes(), saves[i]...)
		buffers = append(buffers, buffer)
	}
	return buffers
}

func makeSavestateReqPackets() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeSavestateReq)
	return buf.Bytes()
}

// Encodes the player input state into a compact form for network transmission.
func encodeInput(st input.PlayerState) uint32 {
	var out uint32
	for i, b := range st {
		if b {
			out |= (1 << i)
		}
	}
	return out
}

// Decodes the input from a packet generated by encodeInput().
func decodeInput(in uint32) input.PlayerState {
	st := input.PlayerState{}
	for i := range st {
		st[i] = in&(1<<i) > 0
	}
	return st
}

// Splits a slice into multiple slices
func split(buf []byte, lim int) [][]byte {
	var chunk []byte
	chunks := make([][]byte, 0, len(buf)/lim+1)
	for len(buf) >= lim {
		chunk, buf = buf[:lim], buf[lim:]
		chunks = append(chunks, chunk)
	}
	if len(buf) > 0 {
		chunks = append(chunks, buf[:len(buf)])
	}
	return chunks
}

// Joins savestate packets and returns the tick
func joinSave(chunks []SavestatePacket) ([]byte, int64) {
	var result []byte
	currentIndex := 1
	tick := int64(0)
	for i := 0; len(chunks) > 0; i++ {
		savePacket := chunks[i]
		index := int(savePacket.index)
		tick = savePacket.tick
		if index == currentIndex {
			packet := savePacket.save
			result = append(result, packet...)
			chunks[i] = chunks[len(chunks)-1]
			chunks = chunks[:len(chunks)-1]
			i = -1
			currentIndex++
		}
	}
	return result, tick
}

func currentTimestamp() int64 {
    return time.Now().UnixNano() / int64(time.Millisecond)
}
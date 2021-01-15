package netplay

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"time"

	"github.com/libretro/ludo/input"
	ntf "github.com/libretro/ludo/notifications"
	"github.com/libretro/ludo/state"
)

const inputDelayFrames = 3
const historySize = int64(60)
const sendHistorySize = 5

// Network code indicating the type of message.
const (
	MsgCodeHandshake   = byte(1) // Used when sending the hand shake.
	MsgCodePlayerInput = byte(2) // Sends part of the player's input buffer.
	MsgCodePing        = byte(3) // Used to tracking packet round trip time. Expect a "Pong" back.
	MsgCodePong        = byte(4) // Sent in reply to a Ping message for testing round trip time.
	MsgCodeSync        = byte(5) // Used to pass sync data
	MsgCodeQuit        = byte(6)
	MsgCodePause       = byte(7)
	MsgCodeResume      = byte(8)
)

var conn *net.UDPConn // conn is the connection between two players
var connectedToClient = false
var confirmedTick = int64(0)
var localSyncData = uint32(0)
var remoteSyncData = uint32(0)
var isStateDesynced = false
var localSyncDataTick = int64(-1)
var remoteSyncDataTick = int64(-1)
var localTickDelta = int64(0)
var remoteTickDelta = int64(0)
var localInputHistory = [historySize]uint32{}
var remoteInputHistory = [historySize]uint32{}
var localAddr *net.UDPAddr
var remoteAddr net.Addr
var lastSyncedTick = int64(-1)
var messages chan []byte
var inputPoll, gameUpdate func()
var romCRC uint32

// Init initialises a netplay session between two players
func Init(gamePath string, pollCb, updateCb func()) {
	state.Global.Tick = 0
	romCRC = getROMCRC(gamePath)
	inputPoll = pollCb
	gameUpdate = updateCb
	messages = make(chan []byte, 256)

	go func() {
		if err := UDPHolePunching(); err != nil {
			ntf.DisplayAndLog(ntf.Error, "Netplay", err.Error())
		}
	}()

	go listen()
}

// Get input from the remote player for the passed in game tick.
func getRemoteInputState(tick int64) input.PlayerState {
	if tick > confirmedTick {
		// Repeat the last confirmed input when we don't have a confirmed tick
		tick = confirmedTick
		log.Println("Predict:", confirmedTick, remoteInputHistory[(historySize+tick)%historySize])
	}
	return decodeInput(remoteInputHistory[(historySize+tick)%historySize])
}

// Get input state for the local client
func getLocalInputState(tick int64) input.PlayerState {
	return decodeInput(localInputHistory[(historySize+tick)%historySize])
}

// Send the inputState for the local player to the remote player for the given game tick.
func sendInputData(tick int64) {
	// Don't send input data when not connect to another player's game client.
	if !connectedToClient {
		return
	}
	sendPacket(makeInputPacket(tick), 1)
}

func setLocalInput(st input.PlayerState, tick int64) {
	encodedInput := encodeInput(st)
	localInputHistory[(historySize+tick)%historySize] = encodedInput
}

func setRemoteEncodedInput(encodedInput uint32, tick int64) {
	remoteInputHistory[(historySize+tick)%historySize] = encodedInput
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
	_, err := conn.WriteTo(packet, remoteAddr)
	if err != nil {
		log.Println(err)
	}
}

// Listen on the UDP connection
func listen() {
	for {
		if conn == nil {
			continue
		}
		buffer := make([]byte, 1024)
		n, err := conn.Read(buffer)
		if err != nil {
			log.Println(err)
			return
		}
		messages <- buffer[:n]
	}
}

// Checks the queue for any incoming packets and process them.
func receiveData() {
	// For now we'll process all packets every frame.
	for {
		select {
		case data := <-messages:
			r := bytes.NewReader(data)
			var code byte
			binary.Read(r, binary.LittleEndian, &code)

			switch code {
			case MsgCodeHandshake:
				if !connectedToClient {
					ntf.DisplayAndLog(ntf.Success, "Netplay", "Connected")
					connectedToClient = true
				}
			case MsgCodePlayerInput:
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
						log.Println("Received packet with a tick too far ahead. Last: ", confirmedTick, " Current: ", receivedTick)
					}

					confirmedTick = receivedTick

					for offset := int64(sendHistorySize - 1); offset >= 0; offset-- {
						var encodedInput uint32
						binary.Read(r, binary.LittleEndian, &encodedInput)
						// Save the input history sent in the packet.
						setRemoteEncodedInput(encodedInput, receivedTick-offset)
					}
				}
			case MsgCodePing:
				var pingTime int64
				binary.Read(r, binary.LittleEndian, &pingTime)
				sendPacket(makePongPacket(time.Unix(pingTime, 0)), 1)
			case MsgCodePong:
				var pongTime int64
				binary.Read(r, binary.LittleEndian, &pongTime)
			case MsgCodeSync:
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
			case MsgCodeQuit:
				if !connectedToClient {
					return
				}
				ntf.DisplayAndLog(ntf.Info, "Netplay", "The other player left")
				conn.Close()
				state.Global.Netplay = false
				connectedToClient = false
			case MsgCodePause:
				if state.Global.Paused {
					return
				}
				ntf.DisplayAndLog(ntf.Info, "Netplay", "The other player paused the session")
				state.Global.Paused = true
			case MsgCodeResume:
				if !state.Global.Paused {
					return
				}
				ntf.DisplayAndLog(ntf.Info, "Netplay", "The other player resumed the session")
				state.Global.Paused = false
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
	for i := int64(0); i < sendHistorySize; i++ {
		encodedInput := localInputHistory[(historySize+historyIndexStart+i)%historySize]
		binary.Write(buf, binary.LittleEndian, encodedInput)
	}

	return buf.Bytes()
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

func makeQuitPacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeQuit)
	return buf.Bytes()
}

// SendQuit notifies the pair that we closed the game
func SendQuit() {
	sendPacket(makeQuitPacket(), 5)
}

func makePausePacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodePause)
	return buf.Bytes()
}

// SendPause notifies the pair that we closed the game
func SendPause() {
	sendPacket(makePausePacket(), 5)
}

func makeResumePacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeResume)
	return buf.Bytes()
}

// SendResume notifies the pair that we closed the game
func SendResume() {
	sendPacket(makeResumePacket(), 5)
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

package netplay

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io/ioutil"
	"net"
	"path/filepath"
	"strconv"

	"github.com/libretro/ludo/input"
	ntf "github.com/libretro/ludo/notifications"
)

// Network code indicating the type of message.
const (
	MsgCodeJoin   = byte(1) // Create or join a netplay room
	MsgCodeOwnIP  = byte(2) // Get to know your own external IP as well as your player index
	MsgCodePeerIP = byte(3) // Get the IP of your peer, along with its player index
)

// getROMCRC returns the CRC32 sum of the rom
func getROMCRC(f string) uint32 {
	ext := filepath.Ext(f)
	switch ext {
	case ".zip":
		z, _ := zip.OpenReader(f)
		defer z.Close()
		return z.File[0].CRC32
	default:
		bytes, _ := ioutil.ReadFile(f)
		return crc32.ChecksumIEEE(bytes)
	}
}

func rdvReceiveData(rdv *net.UDPConn) error {
	buffer := make([]byte, 1024)
	n, err := rdv.Read(buffer)
	if err != nil {
		return err
	}
	data := buffer[:n]

	r := bytes.NewReader(data)

	var code byte
	err = binary.Read(r, binary.LittleEndian, &code)
	if err != nil {
		return err
	}

	switch code {

	case MsgCodeOwnIP:
		var playerIndex byte
		binary.Read(r, binary.LittleEndian, &playerIndex)
		input.LocalPlayerPort = uint(playerIndex)

		_, myPortStr, err := net.SplitHostPort(string(data[2:]))
		if err != nil {
			return err
		}
		myPort, err := strconv.ParseInt(myPortStr, 10, 64)
		if err != nil {
			return err
		}
		localAddr = &net.UDPAddr{
			IP:   net.ParseIP("0.0.0.0"),
			Port: int(myPort),
		}

		ntf.DisplayAndLog(ntf.Info, "Netplay", "Waiting for the second player to join")

		return nil

	case MsgCodePeerIP:
		var playerIndex byte
		binary.Read(r, binary.LittleEndian, &playerIndex)
		input.RemotePlayerPort = uint(playerIndex)

		peerIP, peerPortStr, err := net.SplitHostPort(string(data[2:]))
		if err != nil {
			return err
		}
		peerPort, err := strconv.ParseInt(peerPortStr, 10, 64)
		if err != nil {
			return err
		}
		remoteAddr = &net.UDPAddr{
			IP:   net.ParseIP(peerIP),
			Port: int(peerPort),
		}

		if err := rdv.Close(); err != nil {
			return err
		}

		conn, err = net.ListenUDP("udp", localAddr)
		if err != nil {
			return err
		}
		ntf.DisplayAndLog(ntf.Info, "Netplay", "Listening on %s", conn.LocalAddr().String())

		conn.SetReadBuffer(1048576)

		ntf.DisplayAndLog(ntf.Info, "Netplay", "Sending handshake")
		sendPacket(makeHandshakePacket(), 5)

		return nil

	default:
		return errors.New("Unknown packet type")
	}
}

// Join the hole punching server
func makeJoinPacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, MsgCodeJoin)
	binary.Write(buf, binary.LittleEndian, romCRC)
	return buf.Bytes()
}

// UDPHolePunching attempt to traverse the NAT
func UDPHolePunching() error {
	rdv, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("13.50.239.171"),
		Port: 1234,
	})
	if err != nil {
		return err
	}
	rdv.SetReadBuffer(1048576)

	if _, err := rdv.Write(makeJoinPacket()); err != nil {
		return err
	}

	ntf.DisplayAndLog(ntf.Info, "Netplay", "Connected to the relay")

	// Process the rdv connection data until conn is initialized
	for conn == nil {
		if err = rdvReceiveData(rdv); err != nil {
			return err
		}
	}

	return nil
}

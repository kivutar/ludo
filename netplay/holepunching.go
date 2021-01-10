package netplay

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io/ioutil"
	"log"
	"net"
	"path/filepath"
	"strconv"

	"github.com/libretro/ludo/input"
)

const (
	msgJoin      = byte(1)
	msgOwnIP     = byte(2)
	msgPeerIP    = byte(3)
	msgHandshake = byte(4)
)

var holePunched bool

// getROMCRC returns the CRC32 sum of the rom
func getROMCRC(f string) uint32 {
	ext := filepath.Ext(f)
	switch ext {
	case ".zip":
		// Open the ZIP archive
		z, _ := zip.OpenReader(f)
		defer z.Close()
		return z.File[0].CRC32
	default:
		bytes, _ := ioutil.ReadFile(f)
		return crc32.ChecksumIEEE(bytes)
	}
}

func makeJoinPacket() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, msgJoin)
	binary.Write(buf, binary.LittleEndian, romCRC)
	return buf.Bytes()
}

func receive(conn *net.UDPConn) error {
	buffer := make([]byte, 1024)
	n, err := conn.Read(buffer)
	if err != nil {
		return err
	}
	data := buffer[:n]

	log.Println("Received", data)

	r := bytes.NewReader(data)

	var code byte
	err = binary.Read(r, binary.LittleEndian, &code)
	if err != nil {
		return err
	}

	switch code {
	case msgOwnIP:
		var playerID byte
		binary.Read(r, binary.LittleEndian, &playerID)
		input.LocalPlayerPort = uint(playerID)

		addr := string(data[2:])
		log.Println("I am", addr)

		_, myPortStr, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		myPort, err := strconv.ParseInt(myPortStr, 10, 64)
		if err != nil {
			return err
		}
		selfAddr = &net.UDPAddr{
			IP:   net.ParseIP("0.0.0.0"),
			Port: int(myPort),
		}

		return nil
	case msgPeerIP:
		var playerID byte
		binary.Read(r, binary.LittleEndian, &playerID)
		input.RemotePlayerPort = uint(playerID)

		addr := string(data[2:])
		log.Println("I see", addr)

		peerIP, peerPortStr, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		peerPort, err := strconv.ParseInt(peerPortStr, 10, 64)
		if err != nil {
			return err
		}
		clientAddr = &net.UDPAddr{
			IP:   net.ParseIP(peerIP),
			Port: int(peerPort),
		}

		holePunched = true
		return conn.Close()
	}

	return nil
}

func punch() (*net.UDPConn, error) {
	rdv, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 1234,
	})
	if err != nil {
		return nil, err
	}

	rdv.SetReadBuffer(1048576)

	if _, err := rdv.Write(makeJoinPacket()); err != nil {
		return nil, err
	}

	for !holePunched {
		if err = receive(rdv); err != nil {
			return nil, err
		}
	}

	p2p, err := net.ListenUDP("udp", selfAddr)
	if err != nil {
		return nil, err
	}
	p2p.SetReadBuffer(1048576)

	return p2p, nil
}

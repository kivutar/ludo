package netplay

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"strconv"

	"github.com/libretro/ludo/input"
)

const (
	msgHi = byte(1)
	msgIP = byte(2)
)

func makeHi() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, msgHi)
	return buf.Bytes()
}

func receiveReply(conn *net.UDPConn) (uint, string, error) {
	buffer := make([]byte, 1024)
	n, err := conn.Read(buffer)
	if err != nil {
		return 0, "", err
	}
	data := buffer[:n]

	log.Println("Received", data)

	r := bytes.NewReader(data)

	var code byte
	var playerID byte
	var addr []byte
	binary.Read(r, binary.LittleEndian, &code)
	if code == msgIP {
		binary.Read(r, binary.LittleEndian, &playerID)
		addr = data[2:]
		return uint(playerID), string(addr), nil
	}

	return 0, "", nil
}

func punch() (*net.UDPConn, net.Addr) {
	rdv, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("195.201.56.250"),
		Port: 1234,
	})
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}

	rdv.SetReadBuffer(1048576)

	_, err = rdv.Write(makeHi())
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}

	myIdx, my, err := receiveReply(rdv)
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}
	log.Println("I am", my)
	input.LocalPlayerPort = myIdx

	_, myPortStr, err := net.SplitHostPort(my)
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}
	myPort, _ := strconv.ParseInt(myPortStr, 10, 64)

	peerIdx, peer, err := receiveReply(rdv)
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}
	log.Println("I see", peer)
	input.RemotePlayerPort = peerIdx

	peerIP, peerPortStr, err := net.SplitHostPort(peer)
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}
	peerPort, _ := strconv.ParseInt(peerPortStr, 10, 64)
	peerAddr := &net.UDPAddr{
		IP:   net.ParseIP(peerIP),
		Port: int(peerPort),
	}

	rdv.Close()

	p2p, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP("0.0.0.0"),
		Port: int(myPort),
	})
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}
	log.Println("Listening on", p2p.LocalAddr())

	p2p.SetReadBuffer(1048576)

	log.Println("Sending hello")
	_, err = p2p.WriteTo(makeHi(), peerAddr)
	if err != nil {
		log.Println(err.Error())
		return nil, nil
	}

	for {
		_, msg, _ := receiveReply(p2p)
		log.Println(msg)
		return p2p, peerAddr
	}
}

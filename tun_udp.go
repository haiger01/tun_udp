package main

import (
	"github.com/matishsiao/go_reuseport"
	"github.com/songgao/water"
	"github.com/vishvananda/netlink"
	"golang.org/x/net/ipv4"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
)

const (
	tunName string = "tun0"
	txQLen  int    = 5000
	mtu     int    = 1500
)

var serverTunIP net.IP = []byte{10, 0, 1, 254}
var serverUDPIP string = "192.168.1.120"
var serverUDPPort string = "5110"

var clientTunIP net.IP = []byte{10, 0, 1, 1}
var clientUDPIP string = "192.168.1.90"
var clientUDPPort string = "5120"

type DataPacket struct {
	data      []byte
	packetLen int
}

type SocketMessages struct {
	messages []ipv4.Message
	msgCount int
}

var packetPool *sync.Pool
var messagesPool *sync.Pool

var tunInterface *water.Interface
var udpListenConn *ipv4.PacketConn
var udpWriterConn net.PacketConn
var tunLink netlink.Link
var tunReadChan chan *DataPacket = make(chan *DataPacket, 1000)
var udpReadChan chan *SocketMessages = make(chan *SocketMessages, 1000)

func initPacketPool() {
	packetPool = &sync.Pool{
		New: func() interface{} {
			return &DataPacket{data: make([]byte, mtu), packetLen: 0}
		},
	}
}

func initMessagesPool() {
	messagesPool = &sync.Pool{
		New: func() interface{} {
			ms := make([]ipv4.Message, 25)
			for i := 0; i < len(ms); i++ {
				ms[i] = ipv4.Message{
					OOB:     make([]byte, 10),
					Buffers: [][]byte{make([]byte, mtu)},
				}
			}
			return &SocketMessages{messages: ms, msgCount: 0}
		},
	}
}

func init() {
	initPacketPool()
	initMessagesPool()
}

func getDataPacket() *DataPacket {
	return packetPool.Get().(*DataPacket)
}

func putDataPacket(p *DataPacket) {
	packetPool.Put(p)
}

func getSocketMessages() *SocketMessages {
	return messagesPool.Get().(*SocketMessages)
}

func putSocketMessages(p *SocketMessages) {
	messagesPool.Put(p)
}

func runTunReadThread() {
	go func() {
		var packet = make([]byte, mtu)
		for {
			plen, err := tunInterface.Read(packet)
			if err != nil {
				log.Fatal("Tun Interface Read: type unknown %+v\n", err)
			}
			dataPacket := getDataPacket()
			copy(dataPacket.data, packet[:plen])
			dataPacket.packetLen = plen
			tunReadChan <- dataPacket
			//log.Printf("TUN packet received\n")
		}
	}()
}

func runTunWriteThread() {
	go func() {
		for ms := range udpReadChan {
			for i := 0; i < ms.msgCount; i++ {
				plen := ms.messages[i].N
				if 0 == plen {
					continue
				}
				_, err := tunInterface.Write(ms.messages[i].Buffers[0][:plen])
				if err != nil {
					log.Fatal("Tun Interface Write: type unknown %+v\n", err)
				}
				//log.Printf("TUN packet sent\n")
			}
		}
	}()
}

func createTun(ip net.IP) {
	var tunNetwork *net.IPNet = &net.IPNet{IP: ip, Mask: []byte{255, 255, 255, 0}}

	var config = water.Config{
		DeviceType: water.TUN,
	}

	var err error
	tunInterface, err = water.New(config)
	if nil != err {
		log.Fatal("Tun interface init(), Unable to allocate TUN interface: %+v\n", err)
	}

	link, err := netlink.LinkByName(tunName)
	if nil != err {
		log.Fatal("Tun interface %s Up(), Unable to get interface info %+v\n", tunName, err)
	}
	err = netlink.LinkSetMTU(link, mtu)
	if nil != err {
		log.Fatal("Tun interface %s Up() Unable to set MTU to %d on interface\n", tunName, mtu)

	}
	err = netlink.LinkSetTxQLen(link, txQLen)
	if nil != err {
		log.Fatal("Tun interface %s Up() Unable to set MTU to %d on interface\n", tunName, mtu)
	}
	err = netlink.AddrAdd(link, &netlink.Addr{
		IPNet: tunNetwork,
		Label: "",
	})
	if nil != err {
		log.Fatal("Tun interface %s Up() Unable to set IP to %s / %s on interface: %+v\n", tunName, tunNetwork.IP.String(), tunNetwork.String(), err)
	}

	err = netlink.LinkSetUp(link)
	if nil != err {
		log.Fatal("Tun interface Up() Unable to UP interface\n")
	}
	tunLink = link
	log.Printf("Tun interface %s Up() Tun(%s) interface with %s\n", tunName, tunNetwork.IP.String(), tunNetwork.String())
}

func runUDPReadThread() {
	go func() {
		for {
			ms := getSocketMessages()
			msgCount, err := udpListenConn.ReadBatch(ms.messages, syscall.MSG_WAITFORONE)
			if err != nil {
				log.Fatal("UDP Interface Read: type unknown %+v\n", err)
			}
			ms.msgCount = msgCount
			udpReadChan <- ms
			//log.Printf("UDP packets received\n")
		}
	}()
}

func runUDPWriteThread(addrStr string) {
	addr, err := net.ResolveUDPAddr("", addrStr)
	if err != nil {
		log.Fatal("Unable to resolve UDP address %s: %+v\n", addrStr, err)
	}

	go func() {
		for pkt := range tunReadChan {
			_, err := udpWriterConn.WriteTo(pkt.data[:pkt.packetLen], addr)
			putDataPacket(pkt)
			if err != nil {
				log.Fatal("UDP Interface Write: type unknown %+v\n", err)
			}
			//log.Printf("UDP packet sent\n")
		}
	}()
}

func createUDPListener(addrStr string) {
	conn, err := reuseport.NewReusableUDPPortConn("udp", addrStr)
	if err != nil {
		log.Fatal("Unable to open UDP listening socket for addr %s: %+v\n", addrStr, err)
	}
	log.Printf("Listening UDP: %s\n", addrStr)
	udpListenConn = ipv4.NewPacketConn(conn)
}

func createUDPWriter(addrStr string) {
	conn, err := reuseport.NewReusableUDPPortConn("udp", addrStr)
	if err != nil {
		log.Fatal("Unable to open UDP writing socket for addr %s: %+v\n", addrStr, err)
	}
	log.Printf("UDP writing conn: %s\n", addrStr)
	udpWriterConn = conn
}

func usageString() {
	log.Fatal("Usage: %s server|client\n", os.Args[0])
}

func main() {
	argc := len(os.Args)
	if argc < 2 {
		usageString()
	}

	switch os.Args[1] {
	case "server":
		createTun(serverTunIP)
		createUDPListener(serverUDPIP + ":" + serverUDPPort)
		createUDPWriter(serverUDPIP + ":" + clientUDPPort)
		runTunReadThread()
		runUDPReadThread()
		runUDPWriteThread(clientUDPIP + ":" + clientUDPPort)
		runTunWriteThread()
	case "client":
		createTun(clientTunIP)
		createUDPListener(clientUDPIP + ":" + clientUDPPort)
		createUDPWriter(clientUDPIP + ":" + serverUDPPort)
		runTunReadThread()
		runUDPReadThread()
		runUDPWriteThread(serverUDPIP + ":" + serverUDPPort)
		runTunWriteThread()
	default:
		usageString()
	}

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

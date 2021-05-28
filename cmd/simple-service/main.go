package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	discovery "github.com/libp2p/go-libp2p-discovery"
	host "github.com/libp2p/go-libp2p-host"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/multiformats/go-multiaddr"
)

func NewHost(ctx context.Context, port int) (host.Host, error) {

	// Gen private key for host
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		panic(err)
	}
	// Gen adr for peer
	addr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))

	// Create Host (adr + private key)
	return libp2p.New(ctx,
		libp2p.ListenAddrs(addr),
		libp2p.Identity(priv),
	)
}

func run(h host.Host, cancel func()) {
	c := make(chan os.Signal, 1)

	signal.Notify(c, os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	<-c

	fmt.Printf("\rExiting...\n")

	cancel()

	if err := h.Close(); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func JoinByTopic(ctx context.Context, ps *pubsub.PubSub, selfID peer.ID, _topic string) (sub *pubsub.Subscription) {
	// join the pubsub topic
	topic, err := ps.Join(_topic)
	if err != nil {
		panic(err)
	}

	// and subscribe to it
	sub, err = topic.Subscribe()
	if err != nil {
		panic(err)
	}
	return
}

func Discover(ctx context.Context, h host.Host, dht *dht.IpfsDHT, rendezvous string) {

	//
	// ===== Announce your presence using a rendezvous point =====
	//With the DHT set up, it’s time to discover other peers

	//
	//	The Advertise function starts a go-routine that keeps on advertising until the context gets cancelled.
	//	It announces its presence every 3 hours. This can be shortened by providing a TTL (time to live) option as a fourth parameter.
	//
	//	routingDiscovery.Advertise makes this node announce that it can provide a value for the given key.
	//	Where a key in this case is rendezvousString. Other peers will hit the same key to find other peers.

	// We use a rendezvous point "meet me here" to announce our location.
	// This is like telling your friends to meet you at the Eiffel Tower.

	log.Printf("Announcing ourselves...")
	var routingDiscovery = discovery.NewRoutingDiscovery(dht)
	discovery.Advertise(ctx, routingDiscovery, rendezvous)
	log.Printf("Successfully announced!")

	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Printf("Find advertised peers by rendezvous....")
			//log.Printf("Current connected peers: %s", h.Network().Peers())
			// Now, look for others who have announced
			// This is like your friend telling you the location to meet you.
			peers, err := discovery.FindPeers(ctx, routingDiscovery, rendezvous)
			if err != nil {
				log.Fatal(err)
			}
			for _, p := range peers {
				if p.ID == h.ID() {
					continue
				}
				log.Printf("Founded peer through discovery && rendezvous : %s, is connected: %s", p, h.Network().Connectedness(p.ID) != network.Connected)
				if h.Network().Connectedness(p.ID) != network.Connected {
					_, err := h.Network().DialPeer(ctx, p.ID)
					if err != nil {
						continue
					}

					log.Printf("========================================== Connected to peer:", p)
				}
			}
			log.Printf("=============================================================")
		}
	}
}

func ListPeersUnderPubSub(ps *pubsub.PubSub, topic string) []peer.ID {
	return ps.ListPeers(topic)
}

func main() {

	rendezvousString := "testVVV"
	my_topic := "my_topic2"

	ctx, cancel := context.WithCancel(context.Background())
	port := 5558

	// ======== 1. CREATE HOST ========
	//
	h, err := NewHost(ctx, port)
	if err != nil {
		panic(err)
	}

	log.Printf("Host ID: %s", h.ID().Pretty())
	//log.Printf("This ID reacheble for connect? : %s", h.Network().Connectedness(h.ID()))
	log.Printf("Other should be connect to me on:")
	for _, addr := range h.Addrs() {
		log.Printf("  %s/p2p/%s", addr, h.ID().Pretty())
	}

	// ======== 2. CREATE DHT ========
	//

	// Start a DHT, for use in peer discovery. We can't just make a new DHT
	// client because we want each peer to maintain its own local copy of the
	// DHT, so that the bootstrapping node of the DHT can go down without
	// inhibiting future peer discovery.
	kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		panic(err)
	}

	// Bootstrap the DHT. In the default configuration, this spawns a Background
	// thread that will refresh the peer table every five minutes.
	log.Printf("Bootstrapping the DHT")
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		panic(err)
	}

	// These nodes are used to find nearby peers using DHT.
	// Let's connect to the bootstrap nodes first. They will tell us about the
	// other nodes in the network.
	// At this point we are knowing about DHT but our host.ID does't include in DHT
	bootstrapPeers := dht.DefaultBootstrapPeers

	var wg sync.WaitGroup
	for _, peerAddr := range bootstrapPeers {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		log.Printf("Finded peer from DHT table: %s", peerinfo)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Connect(ctx, *peerinfo); err != nil {
				log.Printf("Error: ", err)
			} else {
				log.Printf("Connection established with bootstrap node:", *peerinfo)
			}
		}()
	}
	wg.Wait()

	//
	// ======== 3. INIT PUBSUB ========
	//

	// create a new PubSub service using the GossipSub router
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		panic(err)
	}

	//
	// ======== 4. DISCOVER ========
	//

	go Discover(ctx, h, kademliaDHT, rendezvousString)

	//
	// ======== 4. COLLECT BY TOPIC ========
	//

	// tries to subscribe to the PubSub topic for the room name
	JoinByTopic(ctx, ps, h.ID(), my_topic)

	go func() {
		for {
			//log.Printf("Current connected peers: %s", h.Network().Peers()[0])
			conns := h.Network().Conns()
			//log.Printf("List Connection of this Network: %s \n", conns)
			for _, conn := range conns {
				neibhorPeerAdr, err := kademliaDHT.FindPeer(ctx, conn.RemotePeer())
				if err != nil {
					panic(err)
				}
				log.Printf("Information of peer on the over side: %s, %s", conn.Stat().Direction, neibhorPeerAdr)

			}
			log.Printf("=====================================================================================\n")
			log.Printf("===>>>>>>>>>>>>>>>>>>>>\n %s", ListPeersUnderPubSub(ps, my_topic))
			time.Sleep(1 * time.Minute)

		}
	}()

	run(h, cancel)
}

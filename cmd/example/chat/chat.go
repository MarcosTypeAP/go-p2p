package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/MarcosTypeAP/go-p2p"
)

//go:generate go run github.com/MarcosTypeAP/go-p2p/cmd/genpackets -prefix=Payload

//p2p:packet -need-ack
const (
	PacketMessage = p2p.PacketCustom + iota
	PacketChangeName
)

//p2p:payload PacketMessage -ref=parser
type PayloadPacketMessage struct {
	Msg []byte
}
type PayloadPacketChangeName struct {
	Name string
}

func main() {
	var peers []string
	flag.Func("peer", "Address of the peer you want to chat with. One flag for each peer.", func(addr string) error {
		peers = append(peers, addr)
		return nil
	})
	var localAddr string
	flag.StringVar(&localAddr, "laddr", "", "Local address for connections.")

	flag.Parse()

	fmt.Println("Peers:", strings.Join(peers, ", "))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)

	go func() {
		<-ctx.Done()
		stop()
	}()

	sendQueues := make([]chan p2p.Packet, 0, len(peers))

	broadcastPacket := func(packet p2p.Packet) {
		packetCopy := make(p2p.Packet, len(packet))
		copy(packetCopy, packet)

		for _, queue := range sendQueues {
			select {
			case queue <- packetCopy:
			default:
				<-queue
				queue <- packetCopy
			}
		}
	}

	wg := sync.WaitGroup{}
	for _, raddr := range peers {
		sendQueue := make(chan p2p.Packet, 16)
		defer close(sendQueue)

		sendQueues = append(sendQueues, sendQueue)

		wg.Add(1)
		go func() {
			defer wg.Done()
			handlePeer(ctx, localAddr, raddr, sendQueue)
		}()
	}

	go func() {
		inputError := func(err error) {
			fmt.Printf("Input error: %v\n", err)
		}

		packetBuf := make([]byte, 512)
		msgBuf := make([]byte, 512)

		for {
			n, err := os.Stdin.Read(msgBuf)
			if err != nil {
				inputError(err)
				continue
			}
			msg := msgBuf[:n-1] // trim \n

			if len(msg) == 0 {
				continue
			}

			if len(msg) > 400 {
				inputError(fmt.Errorf("message too long (max = 400), got %d", len(msg)))
				continue
			}

			if bytes.HasPrefix(msg, []byte("/name")) {
				parts := bytes.SplitN(msg, []byte{' '}, 2)
				if len(parts) == 1 {
					inputError(fmt.Errorf("/name needs a name"))
					continue
				}
				name := bytes.TrimSpace(parts[1])
				if len(name) == 0 {
					inputError(fmt.Errorf("/name needs a name"))
					continue
				}

				broadcastPacket(BuildPacketChangeName(packetBuf, PayloadPacketChangeName{string(name)}))
				continue
			}

			broadcastPacket(BuildPacketMessage(packetBuf, PayloadPacketMessage{msg}))
		}
	}()

	<-ctx.Done()
	wg.Wait()
}

func handlePeer(ctx context.Context, localAddr string, peerAddr string, sendQueue <-chan p2p.Packet) {
	warnInvalidPacket := func(packet p2p.Packet, err error) {
		slog.Warn("Received invalid packet", "peer", peerAddr, "packet", packet, "err", err)
	}

	peerName := ""

Reconnect:
	conn, err := p2p.Dial(ctx, peerAddr, p2p.WithLocalAddr(localAddr), p2p.WithReuseAddress())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		slog.Error("Attempting to connect", "peer", peerAddr, "err", err)
		goto Reconnect
	}
	fmt.Printf("Connected: %s\n", peerAddr)

	msg := PayloadPacketMessage{Msg: make([]byte, 512)}
	conn.OnReceive(PacketMessage, func(packet p2p.Packet) {
		err := ParsePacketMessage(packet, &msg)
		if err != nil {
			warnInvalidPacket(packet, err)
			return
		}

		if peerName == "" {
			fmt.Printf("Message from %s: %s\n", peerAddr, msg.Msg)
		} else {
			fmt.Printf("Message from %s: %s\n", peerName, msg.Msg)
		}
	})
	conn.OnReceive(PacketChangeName, func(packet p2p.Packet) {
		payload, err := ParsePacketChangeName(packet)
		if err != nil {
			warnInvalidPacket(packet, err)
			return
		}

		peerName = payload.Name
	})

Loop:
	for {
		select {
		case <-conn.Done():
			break Loop
		case packet, ok := <-sendQueue:
			if !ok {
				break Loop
			}
			err := <-conn.Send(packet)
			if err != nil {
				break Loop
			}
		}
	}

	<-conn.Done()

	err = conn.Err()
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return

		case errors.Is(err, p2p.ErrPeerClosed):
			if peerName == "" {
				fmt.Printf("Disconnected: %s\n", peerAddr)
			} else {
				fmt.Printf("Disconnected: %q (%s)\n", peerName, peerAddr)
			}

		default:
			slog.Error("Connection closed", "peer", peerAddr, "err", err)
		}
	}

	select {
	case <-ctx.Done():
		return
	default:
		goto Reconnect
	}
}

# go-p2p

<p align="left">
  <a href="https://pkg.go.dev/github.com/MarcosTypeAP/go-p2p"><img src="https://pkg.go.dev/badge/github.com/MarcosTypeAP/go-p2p.svg" alt="Go Reference"></a>
</p>

**go-p2p** is an easy-to-use game-dev datagram-based P2P networking library focused on reducing boilerplate while remaining robust and flexible
which allows you to communicate without external servers, and without worrying about NAT and port forwarding on routers.
It was primarily designed as a game development library, implementing a lightweight version of TCP over UDP,
enabling fast and reliable communication.

> :warning: You should be aware of the limitations of this approach, which doesn't rely on external servers.
Some routers may have stricter rules that prevent you from establishing a connection.

## Features

<!--NOT TESTED YET-->
<!--- **Blazingly Fast**: Not having all the unnecessary features of TCP makes each request much lighter.-->

- **Reliability and Order**: (Optional) It ensures that the packets reaches their destination in the order they were sent.
- **Fully Asynchronous**: The API allows you to send packets without blocking and receive packets by registering handlers for specific packet types.
- **Auto-generated Packets**: You can auto-generate utilities (at pre-compile time) for building and parsing packets by just declaring their payload with `struct`s.

## Installation

#### Import
```go
import "github.com/MarcosTypeAP/go-p2p"
```

#### Download
```bash
$ go mod tidy
# Or
$ go get github.com/MarcosTypeAP/go-p2p

# Install the packet generator (optional)
$ go install github.com/MarcosTypeAP/go-p2p/cmd/genpackets@latest
```

## Usage

<details>
<summary>API Example</summary>

> This is just an example of the API, there is a working example below :)

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/MarcosTypeAP/go-p2p"
)

// Auto-generate utilities
//go:generate genpackets -prefix=Payload -payload-flags=-fields=all

// Group level
//
//p2p:packet -need-ack
const (
	PacketMessage = p2p.PacketCustom + iota

	// Packet level
	PacketPing //p2p:packet -need-ack -exclude=parser
	PacketPong //p2p:packet -need-ack -exclude=parser

	PacketSomething //p2p:packet
	// ...
)

type PayloadPacketMessage struct {
	msg string
}

type Thing struct {
	nested string
}

//p2p:payload PacketSomething -fields=unexported -ref=parser
type PayloadPacketSomething struct {
	ptr    *float32
	random struct {
		a       int
		b       Thing
		mapList []map[int]string
	}
	Exported int
}

func client() {
    // Imagine this is useful
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to 100.100.100.100 (both peers are using the default port)
	conn, err := p2p.Dial(ctx, "100.100.100.100")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Listen for PacketPing and reply with PacketPong
	conn.OnReceive(PacketPing, func(packet p2p.Packet) {
		buf := make([]byte, p2p.HeaderSize)
		conn.Send(BuildPacketPong(buf))
	})

	thing := PayloadPacketSomething{}
	thing.random.mapList = make([]map[int]string, 2)
	for i := range len(thing.random.mapList) {
		thing.random.mapList[i] = make(map[int]string, 4)
	}

	// Listen for PacketSomething and parse it without allocating new memory
	conn.OnReceive(PacketSomething, func(packet p2p.Packet) {
		err = ParsePacketSomething(packet, &thing)
		if err != nil {
			log.Fatal(err)
		}
		for _, m := range thing.random.mapList {
			for k, v := range m {
				log.Println("Some random thing:", k, v)
			}
		}
	})

	msg := PayloadPacketMessage{"some message"}
	buf := make([]byte, 32)

	// Send a PacketMessage and wait for it to arrive
	err = <-conn.Send(BuildPacketMessage(buf, msg))
	if err != nil {
		log.Fatal(err)
	}

	// Wait to receive a PacketMessage
	res := <-conn.Receive(PacketMessage, buf)
	if res.Err != nil {
		log.Fatal(res.Err)
	}
	// And then parse it
	msg, err = ParsePacketMessage(res.Packet)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Received message:", msg.msg)

	// Wait for the connection to end
	<-conn.Done()

	// Check the cause why it ended
	err = conn.Err()
	if err != nil {
		if errors.Is(err, p2p.ErrPeerClosed) {
			log.Println("Peer disconnected")
			return
		}
		log.Fatal(err)
	}
}

func server() {
    // Imagine this is useful
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wait for 200.200.200.200 to connect (both peers are using the default port)
	conn, err := p2p.Listen(ctx, "200.200.200.200", p2p.WithReceiveBufferSize(8), p2p.WithReuseAddress()) // Some random options
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Listen to unexpected packets and exit
	conn.OnReceiveUnexpected(func(packet p2p.Packet) {
		log.Fatalf("Received unexpected packet: kind=%v, payload=%d", packet.Kind(), packet.Payload())
	})

	thing := PayloadPacketSomething{
		random: struct {
			a       int
			b       Thing
			mapList []map[int]string
		}{
			mapList: []map[int]string{
				{},
				{
					69:   "420",
					1337: "leet",
				},
			},
		},
	}
	buf := make([]byte, 128)

	// Send a PacketSomething 3 times because it doesn't have the -need-ack flag (hope at least one arrives)
	thingPacket := BuildPacketSomething(buf, thing)
	for range 3 {
		conn.Send(thingPacket)
	}

	// Wait to receive a PacketMessage
	res := <-conn.Receive(PacketMessage, buf)
	if res.Err != nil {
		log.Fatal(res.Err)
	}
	// And then parse it
	msg, err := ParsePacketMessage(res.Packet)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Received message:", msg.msg)

	// Wait for the connection to end
	<-conn.Done()

	// Check the cause why it ended
	err = conn.Err()
	if err != nil {
		if errors.Is(err, p2p.ErrPeerClosed) {
			log.Println("Peer disconnected")
			return
		}
		log.Fatal(err)
	}
}

func main() {
    // client()
    // or
    // server()
}
```

```bash
// Generate utilities before compiling
$ go generate [<package>]
```

</details>

<details>
<summary>Genpackets Usage</summary>

> :warning: This may be outdated, if you are using it, see it running `genpackets -help`.

```
Genpackets generates builders and parsers for the specified packet kinds.
It looks for comment directives to generate packet utilities.

Usage:

    genpackets [flags]

Flags:
    -path string
        Indicates the file path of the generated code, it will be suffixed with '_gen.go'.
        (absolute or relative to the file where //go:generate was placed) (default "packets")
    -payload-flags flags
        Comma-separated list of payload directive flags passed to structs matched by prefix.
    -prefix string
        Generate utilities for structs that have the prefix followed by packet kind.
        eg: prefix=Payload and kind=PacketPing matches: type PayloadPacketPing struct { ... }
    -register-names
        Indicates that names should be generated for the registered packet kinds. (default true)

Directives:

//p2p:packet [flags]

    Must be placed above or next to a packet kind declaration of type p2p.PacketKind.
    It registers a single variable (or group) as a packet kind and specifies their behavior.
    If it is placed on a declaration group, it applies to all declarations in the group.
    If it is placed on a single declaration, it overwrites the group directive.

    Flags:
        -exclude scope
            This indicates that no utility should be generated for this packet kind and the given scope.
        -need-ack
            This indicates that the receiver needs to send an acknowledgment that it received the packet.
        -register-name
            Indicates that names should be generated for the registered packet kinds. (default true)

    Example:
        //p2p:packet -need-ack
        const (
          PacketA = p2p.PacketCustom + iota
          PacketB //p2p:packet -exclude=builder
          ...
        )

        const PacketC p2p.PacketKind = 69

    Final flags:
        PacketA has -need-ack (from group directive)
        PacketB has -exclude=builder (overwrites group directive)
        PacketC has none (It is not recognized as a valid kind because it does not have the directive)

//p2p:payload PACKET_KIND [flags]

    Must be placed above a struct. It indicates that the struct below represents the payload format of the specified packet kind.
    By default, only a builder is generated for packets without this directive.

    Flags:
        -alloc
            This indicates that memory should be allocated for slices and maps.
            It only affects parsers and it is always 'on' when -ref is not used.
        -exclude scope
            This indicates that no utility should be generated for this packet kind and the given scope. 
        -fields export-type
            This filters which fields are used to generate the utilities given the export-type.
        -ref scope
            This indicates that the struct should be passed by reference to the generated utilities on the given scope. 

    Example:
        //p2p:packet
        const (
          PacketPing = p2p.PacketCustom + iota
          ...
        )

        //p2p:payload PacketPing -ref=parser
        type packetPingPayload struct {
          a int
          b bool
        }

    Generates:
        func BuildPacketPing(buf []byte, payload packetPingPayload) p2p.Packet { ... }
        func ParsePacketPing(packet p2p.Packet, payload *packetPingPayload) (err error) { ... }

    Types:
        Scope = none | builder | parser | all
        Export-type = exported | unexported | all
```
</details>

<details>
<summary>Chat App Example</summary>

The example code can be found [here](https://github.com/MarcosTypeAP/go-p2p/blob/main/cmd/example/chat/chat.go).

### Steps
```bash
$ mkdir chatapp && cd chatapp
$ go mod init chatapp

// Download the chat code example
$ wget https://raw.githubusercontent.com/MarcosTypeAP/go-p2p/refs/heads/main/cmd/example/chat/chat.go

// Download `go-p2p` and `genpackets`
$ go mod tidy
$ go install github.com/MarcosTypeAP/go-p2p/cmd/genpackets@latest

// Generate packet utilities
$ go generate
```

Try it on localhost:
- Type something and hit enter to send a message.
- You can change your name with `/name <name>`

#### Shell 1
Run `go run . -laddr=127.0.0.1:61000 -peer=127.0.0.1:62000 -peer=127.0.0.1:63000`
```
Peers: 127.0.0.1:62000, 127.0.0.1:63000
Connected: 127.0.0.1:62000
Connected: 127.0.0.1:63000
/name juanceto01
bar
Message from 127.0.0.1:62000: foo
Disconnected: 127.0.0.1:63000
Connected: 127.0.0.1:63000
Message from 127.0.0.1:63000: baz
```

#### Shell 2
Run `go run . -laddr=127.0.0.1:62000 -peer=127.0.0.1:61000 -peer=127.0.0.1:63000`
```
Peers: 127.0.0.1:61000, 127.0.0.1:63000
Connected: 127.0.0.1:61000
Connected: 127.0.0.1:63000
Message from juanceto01: bar
foo
Disconnected: 127.0.0.1:63000
Connected: 127.0.0.1:63000
Message from 127.0.0.1:63000: baz
```

#### Shell 3
Run `go run . -laddr=127.0.0.1:63000 -peer=127.0.0.1:61000 -peer=127.0.0.1:62000`
```
Peers: 127.0.0.1:61000, 127.0.0.1:62000
Connected: 127.0.0.1:61000
Connected: 127.0.0.1:62000
Message from juanceto01: bar
Message from 127.0.0.1:62000: foo
^C                                                                                                                                                                           

$ go run . -laddr=127.0.0.1:63000 -peer=127.0.0.1:61000 -peer=127.0.0.1:62000
Peers: 127.0.0.1:61000, 127.0.0.1:62000
Connected: 127.0.0.1:61000
Connected: 127.0.0.1:62000
baz
```
</details>

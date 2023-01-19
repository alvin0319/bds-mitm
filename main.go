package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"golang.org/x/oauth2"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// filteredPackets represents a list of packets that should be filtered out when logging.
// Packet listed in here will NOT be printed to the console.
var filteredPackets = map[string]bool{
	getType(&packet.MovePlayer{}, false):                  true,
	getType(&packet.PlayerAuthInput{}, false):             true,
	getType(&packet.SetActorData{}, false):                true,
	getType(&packet.SetActorMotion{}, false):              true,
	getType(&packet.MoveActorAbsolute{}, false):           true,
	getType(&packet.MoveActorDelta{}, false):              true,
	getType(&packet.MovePlayer{}, false):                  true,
	getType(&packet.SubChunk{}, false):                    true,
	getType(&packet.SubChunkRequest{}, false):             true,
	getType(&packet.ActorEvent{}, false):                  true,
	getType(&packet.AvailableCommands{}, false):           true,
	getType(&packet.StartGame{}, false):                   true,
	getType(&packet.BiomeDefinitionList{}, false):         true,
	getType(&packet.InventoryContent{}, false):            true,
	getType(&packet.InventoryTransaction{}, false):        true,
	getType(&packet.InventorySlot{}, false):               true,
	getType(&packet.CreativeContent{}, false):             true,
	getType(&packet.AddActor{}, false):                    true,
	getType(&packet.LevelEvent{}, false):                  true,
	getType(&packet.RemoveActor{}, false):                 true,
	getType(&packet.LevelSoundEvent{}, false):             true,
	getType(&packet.SetTime{}, false):                     true,
	getType(&packet.UpdateAttributes{}, false):            true,
	getType(&packet.NetworkChunkPublisherUpdate{}, false): true,
	getType(&packet.LevelChunk{}, false):                  true,
	getType(&packet.CraftingEvent{}, false):               true,
	getType(&packet.CraftingData{}, false):                true,
}

func main() {
	var host string
	var port int

	flag.StringVar(&host, "host", "127.0.0.1", "Host to connect to") // blame minecraft for this
	flag.IntVar(&port, "port", 19134, "Port to connect to")

	log.Println("Binding on 0.0.0.0:19132")
	log.Printf("Connecting to %s:%d\n", host, port)

	hostString := host + ":" + strconv.Itoa(port)

	token, err := tokenSource().Token()
	if err != nil {
		panic(err)
	}
	src := auth.RefreshTokenSource(token)
	p, err := minecraft.NewForeignStatusProvider(hostString)

	if err != nil {
		panic(err)
	}

	listener, err := minecraft.ListenConfig{
		StatusProvider: p,
	}.Listen("raknet", ":19132")
	go func() {
		for {
			var input string
			_, err := fmt.Scanln(&input)
			if err != nil {
				log.Println(err)
				continue
			}
			if input == "stop" {
				listener.Close()
				os.Exit(0)
			}
		}
	}()
	defer listener.Close()
	for {
		c, err := listener.Accept()
		if err != nil {
			log.Printf("An error occurred whilst accepting client: %v\n", err)
			continue
		}
		go func() {
			err := handleConn(c.(*minecraft.Conn), listener, hostString, src)
			if err != nil {
				log.Printf("An error occurred whilst handling client: %v\n", err)
			}
		}()
	}
}

// handleConn accepts the connection from the client and tries to connect to the target server.
func handleConn(conn *minecraft.Conn, listener *minecraft.Listener, hostString string, src oauth2.TokenSource) error {
	serverConn, err := minecraft.Dialer{
		TokenSource: src,
		ClientData:  conn.ClientData(),
	}.Dial("raknet", hostString)
	if err != nil {
		return err
	}
	var g sync.WaitGroup
	g.Add(2)
	go func() {
		if err := conn.StartGame(serverConn.GameData()); err != nil {
			log.Printf("An error occurred whilst starting game: %v\n", err)
		}
		g.Done()
	}()
	go func() {
		if err := serverConn.DoSpawn(); err != nil {
			log.Printf("An error occurred whilst spawning: %v\n", err)
		}
		g.Done()
	}()
	g.Wait()
	go func() {
		defer listener.Disconnect(conn, "connection lost")
		defer serverConn.Close()
		for {
			pk, err := conn.ReadPacket()
			if err != nil {
				return
			}
			onClientPacketReceived(conn, pk)
			if err := serverConn.WritePacket(pk); err != nil {
				if disconnect, ok := errors.Unwrap(err).(minecraft.DisconnectError); ok {
					_ = listener.Disconnect(conn, disconnect.Error())
				}
				return
			}
		}
	}()

	go func() {
		defer serverConn.Close()
		defer listener.Disconnect(conn, "connection lost")
		for {
			pk, err := serverConn.ReadPacket()
			if err != nil {
				if disconnect, ok := errors.Unwrap(err).(minecraft.DisconnectError); ok {
					_ = listener.Disconnect(conn, disconnect.Error())
				}
				return
			}
			onServerPacketReceived(conn, pk)
			if err := conn.WritePacket(pk); err != nil {
				return
			}
		}
	}()
	return nil
}

// onClientPacketReceived is called when a packet is received from the client.
// A Packet which is listed in filteredPackets will be ignored.
func onClientPacketReceived(conn *minecraft.Conn, pk packet.Packet) {
	if p, ok := pk.(*packet.ChangeDimension); ok {
		log.Printf("Received Change Dimension on client with dimension ID %d on time: %s\n", p.Dimension, time.Now().String())
		log.Printf("Additional Data: %v(Respawn), %v(Position)\n", p.Respawn, p.Position)
	} else if p, ok := pk.(*packet.PlayStatus); ok {
		log.Printf("Received Play Status on client with status type %d on time: %s\n", p.Status, time.Now().String())
		log.Printf("Additional Data: %v\n", p.Status)
	} else if p, ok := pk.(*packet.PlayerAction); ok {
		log.Printf("Received Player Action on client with action type %d on time: %s\n", p.ActionType, time.Now().String())
		log.Printf("Additional Data: %v(BlockPosition), %v(BlockFace), %v(ResultPos)\n", p.BlockPosition, p.BlockFace, p.ResultPosition)
	} else if _, ok := pk.(*packet.SetLocalPlayerAsInitialised); ok {
		log.Printf("Received Set Local Player As Initialised on client on time: %s\n", time.Now().String())
	} else {
		t := getType(pk, false)
		if _, ok := filteredPackets[t]; ok {
			return // ignore spam
		}
		log.Printf("Received "+t+" on client on time: %s\n", time.Now().String())
		log.Printf("Additional Data: %v\n", pk)
	}
}

// onServerPacketReceived is called when a packet is received from the server.
// A Packet which is listed in filteredPackets will be ignored.
func onServerPacketReceived(conn *minecraft.Conn, pk packet.Packet) {
	if p, ok := pk.(*packet.ChangeDimension); ok {
		log.Printf("Received Change Dimension on server with dimension ID %d on time: %s\n", p.Dimension, time.Now().String())
		log.Printf("Additional Data: %v(Respawn), %v(Position)\n", p.Respawn, p.Position)
	} else if p, ok := pk.(*packet.PlayStatus); ok {
		log.Printf("Received Play Status on server with status type %d on time: %s\n", p.Status, time.Now().String())
		log.Printf("Additional Data: %v\n", p.Status)
	} else if p, ok := pk.(*packet.PlayerAction); ok {
		log.Printf("Received Player Action on server with action type %d on time: %s\n", p.ActionType, time.Now().String())
		log.Printf("Additional Data: %v(BlockPosition), %v(BlockFace), %v(ResultPos)\n", p.BlockPosition, p.BlockFace, p.ResultPosition)
	} else if _, ok := pk.(*packet.SetLocalPlayerAsInitialised); ok {
		log.Printf("Received Set Local Player As Initialised on server on time: %s\n", time.Now().String())
	} else {
		t := getType(pk, false)
		if _, ok := filteredPackets[t]; ok {
			return // ignore spam
		}
		log.Printf("Received "+t+" on server on time: %s\n", time.Now().String())
		log.Printf("Additional Data: %v\n", pk)
	}
}

// tokenSource returns a token source for using with a gophertunnel client. It either reads it from the
// token.tok file if cached or requests logging in with a device code.
func tokenSource() oauth2.TokenSource {
	check := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	token := new(oauth2.Token)
	tokenData, err := ioutil.ReadFile("token.tok")
	if err == nil {
		_ = json.Unmarshal(tokenData, token)
	} else {
		token, err = auth.RequestLiveToken()
		check(err)
	}
	src := auth.RefreshTokenSource(token)
	_, err = src.Token()
	if err != nil {
		// The cached refresh token expired and can no longer be used to obtain a new token. We require the
		// user to log in again and use that token instead.
		token, err = auth.RequestLiveToken()
		check(err)
		src = auth.RefreshTokenSource(token)
	}
	go func() {
		c := make(chan os.Signal, 3)
		signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
		<-c

		tok, _ := src.Token()
		b, _ := json.Marshal(tok)
		_ = ioutil.WriteFile("token.tok", b, 0644)
		os.Exit(0)
	}()
	return src
}

// getType returns the name of given type.
// https://stackoverflow.com/a/35791105
func getType(myvar interface{}, showPointer bool) string {
	if t := reflect.TypeOf(myvar); t.Kind() == reflect.Ptr {
		if showPointer {
			return "*" + t.Elem().Name()
		} else {
			return t.Elem().Name()
		}
	} else {
		return t.Name()
	}
}

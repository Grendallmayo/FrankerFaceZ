package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"../../server"
	"github.com/abiosoft/ishell"
	"github.com/gorilla/websocket"
)

func commandLineConsole() {

	shell := ishell.NewShell()

	shell.Register("help", func(args ...string) (string, error) {
		shell.PrintCommands()
		return "", nil
	})

	shell.Register("clientcount", func(args ...string) (string, error) {
		server.GlobalSubscriptionLock.RLock()
		count := len(server.GlobalSubscriptionInfo)
		server.GlobalSubscriptionLock.RUnlock()
		return fmt.Sprintln(count, "clients connected"), nil
	})

	shell.Register("globalnotice", func(args ...string) (string, error) {
		msg := server.ClientMessage{
			MessageID: -1,
			Command:   "message",
			Arguments: args[0],
		}
		server.PublishToAll(msg)
		return "Message sent.", nil
	})

	shell.Register("publish", func(args ...string) (string, error) {
		if len(args) < 4 {
			return "Usage: publish [room.sirstendec | _ALL] -1 reload_ff 23", nil
		}

		target := args[0]
		line := strings.Join(args[1:], " ")
		msg := server.ClientMessage{}
		err := server.UnmarshalClientMessage([]byte(line), websocket.TextMessage, &msg)
		if err != nil {
			return "", err
		}

		var count int
		if target == "_ALL" {
			count = server.PublishToAll(msg)
		} else {
			count = server.PublishToChannel(target, msg)
		}
		return fmt.Sprintf("Published to %d clients", count), nil
	})

	shell.Register("memstatsbysize", func(args ...string) (string, error) {
		runtime.GC()

		m := runtime.MemStats{}
		runtime.ReadMemStats(&m)
		for _, val := range m.BySize {
			if val.Mallocs == 0 {
				continue
			}
			shell.Print(fmt.Sprintf("%5d: %6d outstanding (%d total)\n", val.Size, val.Mallocs-val.Frees, val.Mallocs))
		}
		shell.Println(m.NumGC, "collections occurred")
		return "", nil
	})

	shell.Register("authorizeeveryone", func(args ...string) (string, error) {
		if len(args) == 0 {
			if server.Configuration.SendAuthToNewClients {
				return "All clients are recieving auth challenges upon claiming a name.", nil
			}
			return "All clients are not recieving auth challenges upon claiming a name.", nil
		} else if args[0] == "on" {
			server.Configuration.SendAuthToNewClients = true
			return "All new clients will recieve auth challenges upon claiming a name.", nil
		} else if args[0] == "off" {
			server.Configuration.SendAuthToNewClients = false
			return "All new clients will not recieve auth challenges upon claiming a name.", nil
		}
		return "Usage: authorizeeveryone [ on | off ]", nil
	})

	shell.Register("kickclients", func(args ...string) (string, error) {
		if len(args) == 0 {
			return "Please enter either a count or a fraction of clients to kick.", nil
		}
		input, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			return "Argument must be a number", err
		}
		var count int
		if input >= 1 {
			count = int(input)
		} else {
			server.GlobalSubscriptionLock.RLock()
			count = int(float64(len(server.GlobalSubscriptionInfo)) * input)
			server.GlobalSubscriptionLock.RUnlock()
		}

		msg := server.ClientMessage{Arguments: &server.CloseRebalance}
		server.GlobalSubscriptionLock.RLock()
		defer server.GlobalSubscriptionLock.RUnlock()

		kickCount := 0
		for i, cl := range server.GlobalSubscriptionInfo {
			if i >= count {
				break
			}
			select {
			case cl.MessageChannel <- msg:
			case <-cl.MsgChannelIsDone:
			}
			kickCount++
		}
		return fmt.Sprintf("Kicked %d clients", kickCount), nil
	})

	shell.Register("panic", func(args ...string) (string, error) {
		go func() {
			panic("requested panic")
		}()
		return "", nil
	})

	shell.Start()
}

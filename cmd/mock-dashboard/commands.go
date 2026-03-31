package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	agentWs "github.com/kenitech-io/devops-agent/internal/ws"
)

// startCLI runs an interactive command line for sending commands to agents.
// It runs in a separate goroutine.
func startCLI() {
	fmt.Println()
	fmt.Println("Interactive commands:")
	fmt.Println("  list                          - List connected agents")
	fmt.Println("  send <agentId> <action>       - Send a command to an agent")
	fmt.Println("  ping <agentId>                - Send a ping to an agent")
	fmt.Println("  token <token>                 - Add a valid registration token")
	fmt.Println("  help                          - Show this help")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("> ")
			continue
		}
		parts := strings.Fields(line)
		cmd := parts[0]

		switch cmd {
		case "list":
			cliList()
		case "send":
			if len(parts) < 3 {
				fmt.Println("usage: send <agentId> <action> [paramKey=paramValue ...]")
			} else {
				params := parseParams(parts[3:])
				cliSend(parts[1], parts[2], false, params)
			}
		case "stream":
			if len(parts) < 3 {
				fmt.Println("usage: stream <agentId> <action>")
			} else {
				cliSend(parts[1], parts[2], true, nil)
			}
		case "ping":
			if len(parts) < 2 {
				fmt.Println("usage: ping <agentId>")
			} else {
				cliPing(parts[1])
			}
		case "token":
			if len(parts) < 2 {
				fmt.Println("usage: token <token>")
			} else {
				tokensMu.Lock()
				validTokens[parts[1]] = true
				tokensMu.Unlock()
				fmt.Printf("added token: %s\n", parts[1])
			}
		case "help":
			fmt.Println("Commands: list, send, stream, ping, token, help")
			fmt.Println()
			fmt.Println("Actions: container_list, container_stats, container_restart name=X,")
			fmt.Println("  backup_snapshots, backup_stats, backup_trigger, system_disk,")
			fmt.Println("  system_memory, system_info, service_status name=X,")
			fmt.Println("  wireguard_status, docker_logs name=X lines=100")
		default:
			fmt.Printf("unknown command: %s (try 'help')\n", cmd)
		}

		fmt.Print("> ")
	}
}

func cliList() {
	agentsMu.Lock()
	defer agentsMu.Unlock()

	if len(agents) == 0 {
		fmt.Println("no agents connected")
		return
	}

	for _, a := range agents {
		fmt.Printf("  %s  hostname=%s\n", a.ID, a.Hostname)
	}
}

func cliSend(agentID, action string, stream bool, params map[string]interface{}) {
	agentsMu.Lock()
	agent, ok := agents[agentID]
	agentsMu.Unlock()

	if !ok {
		fmt.Printf("agent %s not found\n", agentID)
		return
	}

	var paramsJSON json.RawMessage
	if len(params) > 0 {
		data, _ := json.Marshal(params)
		paramsJSON = data
	}

	payload := agentWs.CommandRequestPayload{
		Action:  action,
		Params:  paramsJSON,
		Stream:  stream,
		Timeout: 30,
	}

	msg, err := agentWs.NewMessage(agentWs.TypeCommandRequest, payload)
	if err != nil {
		fmt.Printf("error creating message: %v\n", err)
		return
	}

	data, _ := json.Marshal(msg)
	if err := agent.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
		fmt.Printf("error sending to agent: %v\n", err)
		return
	}

	log.Printf("sent %s command to agent %s (id=%s)", action, agentID, msg.ID)
}

func cliPing(agentID string) {
	agentsMu.Lock()
	agent, ok := agents[agentID]
	agentsMu.Unlock()

	if !ok {
		fmt.Printf("agent %s not found\n", agentID)
		return
	}

	msg, _ := agentWs.NewMessage(agentWs.TypePing, agentWs.PingPayload{})
	data, _ := json.Marshal(msg)
	if err := agent.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
		fmt.Printf("error sending ping: %v\n", err)
		return
	}
	log.Printf("sent ping to agent %s", agentID)
}

func parseParams(args []string) map[string]interface{} {
	if len(args) == 0 {
		return nil
	}
	params := make(map[string]interface{})
	for _, arg := range args {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) == 2 {
			// Try to parse as number
			var num float64
			if _, err := fmt.Sscanf(parts[1], "%f", &num); err == nil {
				params[parts[0]] = num
			} else {
				params[parts[0]] = parts[1]
			}
		}
	}
	return params
}

package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	giu "github.com/murphysean/gointerfaceutils"
	"golang.org/x/net/websocket"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

func GenUUIDv4() string {
	u := make([]byte, 16)
	rand.Read(u)
	//Set the version to 4
	u[6] = (u[6] | 0x40) & 0x4F
	u[8] = (u[8] | 0x80) & 0xBF
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:])
}

type Message struct {
	Source  string      `json:"source"`
	Command string      `json:"command,omitempty"`
	Path    string      `json:"path"`
	Message interface{} `json:"message"`
	Count   int         `json:"count"`
}

var objects = make(map[string]*Object)

type Object struct {
	Mutex       sync.Mutex
	Json        interface{}
	Subscribers []chan<- Message
}

var sockets = make(map[string]chan<- Message)

func sendMessageToSubs(subs []chan<- Message, message Message) {
	for _, v := range subs {
		v <- message
	}
}

func signalSubs(subs []chan<- Message) {
	for _, v := range subs {
		select {
		case v <- Message{}:
		default:
		}
	}
}

func removeSub(path string, sub chan<- Message) {
	if o, ok := objects[path]; ok {
		tri := -1
		for i, c := range o.Subscribers {
			if sub == c {
				tri = i
				break
			}
		}
		if tri >= 0 {
			o.Subscribers = append(o.Subscribers[:tri], o.Subscribers[tri+1:]...)
		}
	}
}

func closeSubs(subs []chan<- Message) {
	for _, v := range subs {
		close(v)
	}
}

func WSServer(ws *websocket.Conn) {
	name := ""
	if path := ws.Request().URL.Path[len("/ws"):]; strings.HasPrefix(path, "/") && len(path) > 1 {
		//This is a named connection
		name = path[1:]
	}
	//TODO Set this connection up as a listener for system events
	isOpen := true
	if name == "" {
		name = GenUUIDv4()
	}
	if _, ok := sockets[name]; ok {
		ws.Write([]byte("Name already in use"))
		ws.Close()
		return
	}
	fmt.Println("Named Connection for:", name)
	n := make(chan Message, 1)
	defer close(n)
	sockets[name] = n
	defer delete(sockets, name)

	subscriptions := make(map[string]chan Message)

	if o, ok := objects["system"]; ok {
		(&o.Mutex).Lock()
		c := make(chan Message, 1)
		subscriptions["system"] = c
		o.Subscribers = append(o.Subscribers, c)
		defer removeSub("system", c)
		(&o.Mutex).Unlock()
	} else {
		fmt.Println("No Object found for system", "system")
	}

	//Start a go routine to listen
	go func() {
		for isOpen {
			var m Message
			err := websocket.JSON.Receive(ws, &m)
			if err != nil {
				isOpen = false
				fmt.Println("Receiver Closing", err)
				break
			}
			fmt.Println("Recieved", m.Source, m.Command, m.Path)

			//If this is a subscription message, make sure this ws recieves notifications
			if m.Source == "client" && m.Command == "subscribe" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					c := make(chan Message, 1)
					subscriptions[m.Path] = c
					o.Subscribers = append(o.Subscribers, c)
					defer removeSub(m.Path, c)
					(&o.Mutex).Unlock()
				} else {
					fmt.Println("No Object found", m.Path)
				}
			}

			//Publish will just publish the message out to the given channel
			if m.Source == "client" && m.Command == "publish" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					o.Json = m.Message
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json = m.Message
					objects[m.Path] = o
				}
				sendMessageToSubs(objects[m.Path].Subscribers, m)
			}

			//Notify will send a message directly to a websocket
			if m.Source == "client" && m.Command == "notify" && m.Path != "" {
				if s, ok := sockets[m.Path]; ok {
					m.Source = name
					s <- m
				}
			}

			//If this is an object update message
			if m.Source == "client" && m.Command == "merge" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					o.Json, err = giu.MergePatch(o.Json, m.Message)
					if err != nil {
						fmt.Println("MergingError", m.Path, err)
					}
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json = m.Message
					objects[m.Path] = o
				}
				signalSubs(objects[m.Path].Subscribers)
			}

			if m.Source == "client" && m.Command == "patch" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					fmt.Println("Before", o.Json)
					o.Json, err = giu.Patch(o.Json, m.Message)
					if err != nil {
						fmt.Println("PatchingError", m.Path, err)
					}
					fmt.Println("After", o.Json)
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json, err = giu.MergePatch(o.Json, m.Message)
					objects[m.Path] = o
				}
				signalSubs(objects[m.Path].Subscribers)
			}

			if m.Source == "client" && m.Command == "delete" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					closeSubs(objects[m.Path].Subscribers)
					delete(objects, m.Path)
					(&o.Mutex).Unlock()
				}
			}
		}
	}()
	//Block this routine to send
	var count int
	for isOpen {
		select {
		case m, ok := <-n:
			if ok {
				m.Count = count
				err := websocket.JSON.Send(ws, m)
				if err != nil {
					isOpen = false
					fmt.Println("Sender Closing", err)
					break
				}
				fmt.Println("Sent", count)
				count++
			}
		default:
		}
		for k, c := range subscriptions {
			select {
			case _, ok := <-c:
				if ok {
					err := websocket.JSON.Send(ws, Message{"server", "", k, objects[k].Json, count})
					if err != nil {
						isOpen = false
						fmt.Println("Sender Closing", err)
						break
					}
					fmt.Println("Sent", count)
					count++
				} else {
					c = nil
					delete(subscriptions, k)
				}
			default:
			}
			time.Sleep(time.Millisecond * 10)
		}
	}

	for k, c := range subscriptions {
		close(c)
		delete(subscriptions, k)
	}
}

func main() {
	o := new(Object)
	o.Json = map[string]interface{}{"messages": []interface{}{}, "users": []interface{}{}}
	objects["default"] = o
	s := new(Object)
	objects["system"] = s

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Recieved http request", r)
		fmt.Fprintf(w, mainpage)
	})
	http.Handle("/ws", websocket.Handler(WSServer))
	http.Handle("/ws/", websocket.Handler(WSServer))

	//REST API
	http.HandleFunc("/publish/", func(w http.ResponseWriter, r *http.Request) {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method == "POST" && mt == "application/json" {
			path := r.URL.Path[len("/publish/"):]
			if len(path) > 0 {
				m := Message{}
				dec := json.NewDecoder(r.Body)
				err := dec.Decode(&m)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if m.Source == "client" && m.Command == "publish" && m.Path != "" {
					if o, ok := objects[m.Path]; ok {
						(&o.Mutex).Lock()
						o.Json = m.Message
						(&o.Mutex).Unlock()
					} else {
						o := new(Object)
						o.Json = m.Message
						objects[m.Path] = o
					}
					sendMessageToSubs(objects[m.Path].Subscribers, m)
				}
				return
			}
		}
		http.Error(w, "Only POST application/json with typed object supported", http.StatusBadRequest)
	})
	http.HandleFunc("/notify/", func(w http.ResponseWriter, r *http.Request) {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method == "POST" && mt == "application/json" {
			path := r.URL.Path[len("/notify/"):]
			if len(path) > 0 {
				m := Message{}
				dec := json.NewDecoder(r.Body)
				err := dec.Decode(&m)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if m.Source == "client" && m.Command == "notify" && m.Path != "" {
					if s, ok := sockets[m.Path]; ok {
						m.Source = "http-request"
						s <- m
					}
				}
				return
			}
		}
		http.Error(w, "Only POST application/json with typed object supported", http.StatusBadRequest)
	})
	http.HandleFunc("/sharedobject/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/sharedobject/"):]
		if !(len(path) > 0) {
			http.Error(w, "Only POST application/json with typed object supported", http.StatusBadRequest)
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method == "POST" && mt == "application/json" {
			m := Message{}
			dec := json.NewDecoder(r.Body)
			err := dec.Decode(&m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if m.Source == "client" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					o.Json, err = giu.MergePatch(o.Json, m.Message)
					if err != nil {
						fmt.Println("MergingError", m.Path, err)
					}
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json = m.Message
					objects[m.Path] = o
				}
				signalSubs(objects[m.Path].Subscribers)
			}
			return
		} else if r.Method == "PATCH" && mt == "application/json-patch+json" {
			m := Message{}
			dec := json.NewDecoder(r.Body)
			err := dec.Decode(&m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if m.Source == "client" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					fmt.Println("Before", o.Json)
					o.Json, err = giu.Patch(o.Json, m.Message)
					if err != nil {
						fmt.Println("PatchingError", m.Path, err)
					}
					fmt.Println("After", o.Json)
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json, err = giu.MergePatch(o.Json, m.Message)
					objects[m.Path] = o
				}
				signalSubs(objects[m.Path].Subscribers)
			}
		} else if r.Method == "PATCH" && mt == "application/merge-patch+json" {
			m := Message{}
			dec := json.NewDecoder(r.Body)
			err := dec.Decode(&m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if m.Source == "client" && m.Path != "" {
				if o, ok := objects[m.Path]; ok {
					(&o.Mutex).Lock()
					o.Json, err = giu.MergePatch(o.Json, m.Message)
					if err != nil {
						fmt.Println("MergingError", m.Path, err)
					}
					(&o.Mutex).Unlock()
				} else {
					o := new(Object)
					o.Json = m.Message
					objects[m.Path] = o
				}
				signalSubs(objects[m.Path].Subscribers)
			}
			return
		}
		http.Error(w, "Only POST application/json with typed object supported", http.StatusBadRequest)
	})
	fmt.Println(http.ListenAndServe(":8080", nil))
}

var mainpage = `
<!DOCTYPE HTML>
<html>
<head>
<script type="text/javascript">
var ws = null
function WebSocketTest(){
	if(document.getElementById("name-input").value == ""){
		alert("Please enter your name")
		return
	}
	if ("WebSocket" in window){
		console.log("WebSocket is supported!")
		var hp = window.location.hostname
		if(window.location.protocol == "https:"){
			hp = "wss://" + hp
		}else{
			hp = "ws://" + hp
		}
		if(window.location.port != ""){
			hp += ":" + window.location.port
		}
		ws = new WebSocket(hp+"/ws/"+document.getElementById("name-input").value)
		ws.onopen = function(){
			console.log("OPENED")
			ws.send(JSON.stringify({source:"client",command:"subscribe",path:"default"}))
			ws.send(JSON.stringify({source:"client",command:"patch",path:"default",message:[{"op":"add","path":"/users/-","value":document.getElementById("name-input").value}]}))
		}
		ws.onmessage = function(event){
			var m = JSON.parse(event.data)
			var chat = m.message
			console.log(chat)
			var myNode = document.getElementById("lpanel");
			while (myNode.firstChild) {
			    myNode.removeChild(myNode.firstChild);
			}
			chat.users.forEach(function(entry){
				var node = document.createElement("p")
				node.appendChild(document.createTextNode(entry))
				myNode.appendChild(node)
			})
			var myNode = document.getElementById("rpanel");
			while (myNode.firstChild) {
			    myNode.removeChild(myNode.firstChild);
			}
			chat.messages.forEach(function(entry){
				var node = document.createElement("p")
				node.appendChild(document.createTextNode(entry))
				myNode.appendChild(node)
			})
		}
		ws.onclose = function(){
		}
	}else{
		console.log("WebSocket is not supported... :(")
	}
}
function sendMessage(){
	if(ws == null){
		alert("Please connect to websocket first")
		return
	}
	if(document.getElementById("message-input").value == ""){
		alert("Please enter a message to send")
		return
	}
	var message = document.getElementById("name-input").value + ": " + document.getElementById("message-input").value
	ws.send(JSON.stringify({source:"client",command:"patch",path:"default",message:[{op:"add",path:"/messages/-","value":message}]}))
	document.getElementById("message-input").value = ""
}
</script>
<style>
</style>
</head>
<body>
<input id="name-input" type="text"/><br/> 
<a href="javascript:WebSocketTest()">Run Websocket Test</a>
<div id="hbox" style="display:flex;justify-content:center;align-items:center;">
	<div id="lpanel" style="display:flex;flex-direction:column;justify-content:flex-start;align-items:flex-start">
		<p>User1</p>
		<p>User2</p>
	</div>
	<div id="rpanel" style="display:flex;flex-direction:column;justify-content:flex-start;align-items:flex-start">
	<p>Hello</p>
	<p>These will be some chats</p>
	</div>
</div>
<input id="message-input" type="text"/><button onclick="sendMessage()">Send</button>
</body>
</html>
`

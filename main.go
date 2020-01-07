/*
Copyright 2015 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
  "flag"
  "fmt"
  "github.com/kr/pty"
  "golang.org/x/net/websocket"
  "io"
  "log"
  "net/http"
  "os"
  "os/exec"
  "strings"
  "time"
  "bytes"
  "golang.org/x/text/encoding"
  "golang.org/x/text/encoding/unicode"
  "github.com/acarl005/stripansi"
)

var tty *os.File
var debug bool = false
var noauth bool = false
var HEARTBEATCOUNT int = 30
var HEARTBEAT int = 10
var liveCount int
const patternLen = 5
var port string = "32000"
var webAssetsDir string
var passwd string
var wsoc *websocket.Conn

type Handler struct {
  fileServer http.Handler
}

type InputWrapper struct {
  ws *websocket.Conn
}

// ignoredInputs are the strange input bytes that we look for and drop
var ignoredInputs = [][patternLen]byte{
  {27, 91, 62, 48, 59},
  {27, 80, 48, 43, 114},
}

func main() {
  _org          := flag.String("org","http://localhost:8080/","[-org=ORIGIN URL]")
  _ws           := flag.String("ws","ws://localhost:8080/ws","[-ws=WEB SOCKET URL]")
  _passwd       := flag.String("passwd","passwd","[-token=ACCESS PASSWORD]")
  _webAssetsDir := flag.String("html","./www","[-html=HTML DIR]")
  _command      := flag.String("command","/bin/bash","[-command=EXECUTE COMMAND]")
  _port         := flag.String("port","32000","[-port=LISTENING PORT]")
  _debug        := flag.Bool("debug",false,"[-debug=DEBUG MODE]")
  _noauth       := flag.Bool("noauth",false,"[-noauth=DON'T USE BASIC AUTH]")
  _timeout      := flag.Int("timeout",30,"[-timeout=CONNECTION TIMEOUT]")

  flag.Parse()

  org               := string(*_org)
  ws                := string(*_ws)
  passwd             = string(*_passwd)
  webAssetsDir       = string(*_webAssetsDir)
  command           := string(*_command)
  port               = string(*_port)
  debug              = bool(*_debug)
  noauth             = bool(*_noauth)
  HEARTBEATCOUNT     = int(*_timeout)

  if debug == true {
    fmt.Println(" ---------")
    fmt.Println(" |ws     | " + ws)
    fmt.Println(" |passwd | " + passwd)
    fmt.Println(" |html   | " + webAssetsDir)
    fmt.Println(" |command| " + command)
    fmt.Println(" |port   | " + port)
     fmt.Printf(" |debug  | %t\n",debug)
     fmt.Printf(" |noauth | %t\n",noauth)
     fmt.Printf(" |timeout| %d\n",HEARTBEATCOUNT)
    fmt.Println(" ---------")
  }

  // - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = - = -

  wsoc, _ = websocket.Dial(ws, "", org)

  cmd := exec.Command(os.Getenv("SHELL"), "-c",command)
  tty, _ = pty.Start(cmd)
  defer tty.Close()

  fmt.Printf("Server listening on port %s.\n", port)
  http.ListenAndServe("0.0.0.0:"+port, &Handler{
    fileServer: http.FileServer(http.Dir(webAssetsDir)),
  })
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
  if debug == true { 
    log.Printf("%v %v", r.Method, r.URL.Path)
  }

  if noauth == false {
    token := r.URL.Query().Get("token")

    if liveCount > 0 {
      fmt.Fprintf(w, "%s connection exists. timeout count: %d\n", r.RemoteAddr,liveCount)
      return
    }

    if strings.Trim(r.URL.Path, "/") == "" {
      if token != passwd {
        if debug == true { 
          fmt.Printf("token auth error: %s\n", token)
        }
        fmt.Fprintf(w, "token auth error. ")
        return
      }
    }
  }

  // need to serve shell via websocket?
  if strings.Trim(r.URL.Path, "/") == "shell" {

    onShell(w, r)
    return
  }
  // serve static assets from 'static' dir:
  h.fileServer.ServeHTTP(w, r)
}

// GET /shell handler
// Launches /bin/bash and starts serving it via the terminal
func onShell(w http.ResponseWriter, r *http.Request) {
  wsHandler := func(ws *websocket.Conn) {
    // wrap the websocket into UTF-8 wrappers:
    wrapper := NewWebSockWrapper(ws, WebSocketTextMode)
    stdout := wrapper
    stderr := wrapper

    // this one is optional (solves some weird issues with vim running under shell)
    stdin := &InputWrapper{ws}

    // starts new command in a newly allocated terminal:
    // TODO: replace /bin/bash with:
    // kubectl exec -ti <pod> --container <container name> -- /bin/bash
    // pipe to/fro websocket to the TTY:
    go func() {
      io.Copy(stdout, tty)
    }()
    go func() {
      io.Copy(stderr, tty)
    }()
    go func() {
      io.Copy(tty, stdin)
    }()

    // wait for the command to exit, then close the websocket
    //cmd.Wait()

    liveCount = HEARTBEATCOUNT
    for {
      if debug == true { 
        fmt.Printf("%s | live client. count: %d\n", r.RemoteAddr,liveCount)
      }
      if liveCount > 0 { liveCount = liveCount - 1 }
      if liveCount < 1 {
        ws.Close()
        if debug == true {
          fmt.Printf("%s | timeout.\n", r.RemoteAddr)
        }
        return
      }
      time.Sleep(time.Duration(HEARTBEAT) * time.Second)
    }
  }
  defer log.Printf("Websocket session closed for %v", r.RemoteAddr)

  // start the websocket session:
  websocket.Handler(wsHandler).ServeHTTP(w, r)
}

func (this *InputWrapper) Read(out []byte) (n int, err error) {
  var data []byte
  err = websocket.Message.Receive(this.ws, &data)
  if err != nil {
    return 0, io.EOF
  }

  liveCount = HEARTBEATCOUNT

  if len(data) >= patternLen {
    for i := range ignoredInputs {
      pattern := ignoredInputs[i]
      if bytes.Equal(pattern[:], data[:patternLen]) {
        return 0, nil
      }
    }
  }
  return copy(out, data), nil
}

/*
Copyright 2015 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// WebSockWrapper wraps the raw websocket and converts Write() calls
// to proper websocket.Send() working in binary or text mode. If text
// mode is selected, it converts the data passed to Write() into UTF8 bytes
//
// We need this to make sure that the entire buffer in io.Writer.Write(buffer)
// is delivered as a single chunk to the web browser, instead of being split
// into multiple frames. This wrapper basically substitues every Write() with
// Send() and every Read() with Receive()

type WebSockWrapper struct {
	io.ReadWriteCloser

	ws   *websocket.Conn
	mode WebSocketMode

	encoder *encoding.Encoder
	decoder *encoding.Decoder
}

// WebSocketMode allows to create WebSocket wrappers working in text
// or binary mode
type WebSocketMode int

const (
	WebSocketBinaryMode = iota
	WebSocketTextMode
)

func NewWebSockWrapper(ws *websocket.Conn, m WebSocketMode) *WebSockWrapper {
	if ws == nil {
		return nil
	}
	return &WebSockWrapper{
		ws:      ws,
		mode:    m,
		encoder: unicode.UTF8.NewEncoder(),
		decoder: unicode.UTF8.NewDecoder(),
	}
}

// Write implements io.WriteCloser for WebSockWriter (that's the reason we're
// wrapping the websocket)
//
// It replaces raw Write() with "Message.Send()"
func (w *WebSockWrapper) Write(data []byte) (n int, err error) {
	n = len(data)
	if w.mode == WebSocketBinaryMode {
		// binary send:
		err = websocket.Message.Send(w.ws, data)
		// text send:
	} else {
		var utf8 string
		utf8, err = w.encoder.String(string(data))
		err = websocket.Message.Send(w.ws, utf8)
		sendMsg(wsoc, stripansi.Strip(string(utf8)))
	}
	if err != nil {
		n = 0
	}
	return n, err
}

func sendMsg(ws *websocket.Conn, msg string) {
  websocket.JSON.Send(ws, msg)
  if debug == true {
    fmt.Println("Send data=%#v\n", msg)
  }
}

// Read does the opposite of write: it replaces websocket's raw "Read" with
//
// It replaces raw Read() with "Message.Receive()"
func (w *WebSockWrapper) Read(out []byte) (n int, err error) {
	var data []byte

	if w.mode == WebSocketBinaryMode {
		err = websocket.Message.Receive(w.ws, &data)
	} else {
		var utf8 string
		err = websocket.Message.Receive(w.ws, &utf8)
		switch err {
		case nil:
			data, err = w.decoder.Bytes([]byte(utf8))
		case io.EOF:
			return 0, io.EOF
		}
	}
	if err != nil {
		return 0, err
	}
	return copy(out, data), nil
}

func (w *WebSockWrapper) Close() error {
	return w.ws.Close()
}

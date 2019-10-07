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
  _passwd       := flag.String("passwd","passwd","[-token=ACCESS PASSWORD]")
  _webAssetsDir := flag.String("html","/var/www/html","[-html=HTML DIR]")
  _command      := flag.String("command","/bin/bash","[-command=EXECUTE COMMAND]")
  _port         := flag.String("port","32000","[-port=LISTENING PORT]")
  _debug        := flag.Bool("debug",false,"[-debug=DEBUG MODE]")
  _noauth       := flag.Bool("noauth",false,"[-noauth=DON'T USE BASIC AUTH]")
  _timeout      := flag.Int("timeout",30,"[-timeout=CONNECTION TIMEOUT]")

  flag.Parse()

  passwd             = string(*_passwd)
  webAssetsDir       = string(*_webAssetsDir)
  command           := string(*_command)
  port               = string(*_port)
  debug              = bool(*_debug)
  noauth             = bool(*_noauth)
  HEARTBEATCOUNT     = int(*_timeout)

  if debug == true {
    fmt.Println(" ---------")
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

func Exists(name string) bool {
    _, err := os.Stat(name)
    return !os.IsNotExist(err)
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

func execmd(command string) string {
  out, err := exec.Command(os.Getenv("SHELL"), "-c", command + " 2>&1 | tee").Output()
  if err != nil {
    fmt.Println(err)
    return ""
  }
  fmt.Printf("local exec command: %s : %s\n", command, out)
  return strings.Replace(string(out),"\n","",-1)
}

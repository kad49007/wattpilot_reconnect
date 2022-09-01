package wattpilot

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/pbkdf2"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"time"
)

type Wattpilot struct {
	_currentConnection *websocket.Conn
	_requestId         int
	_name              string
	_hostname          string
	_serial            string
	_version           string
	_manufacturer      string
	_devicetype        string
	_protocol          float64
	_secured           bool

	_token3         string
	_hashedpassword string
	_host           string
	_password       string
	_isInitialized  bool
	_status         map[string]interface{}
	eventHandler    map[string]EventFunc
	connected       chan bool
	initialized     chan bool
}

func NewWattpilot(host string, password string) *Wattpilot {
	w := &Wattpilot{
		_host:          host,
		_password:      password,
		connected:      make(chan bool),
		initialized:    make(chan bool),
		_isInitialized: false,
		_requestId:     0,
	}

	w.eventHandler = map[string]EventFunc{
		"hello":          w.onEventHello,
		"authRequired":   w.onEventAuthRequired,
		"response":       w.onEventResponse,
		"authSuccess":    w.onEventAuthSuccess,
		"authError":      w.onEventAuthError,
		"fullStatus":     w.onEventFullStatus,
		"deltaStatus":    w.onEventDeltaStatus,
		"clearInverters": w.onEventClearInverters,
		"updateInverter": w.onEventUpdateInverter,
	}
	return w

}

var done chan interface{}
var interrupt chan os.Signal
var src = rand.New(rand.NewSource(time.Now().UnixNano()))

type EventFunc func(*websocket.Conn, map[string]interface{})

func hasKey(data map[string]interface{}, key string) bool {
	_, isKnown := data[key]
	return isKnown
}

func merge(ms ...map[string]interface{}) map[string]interface{} {
	res := make(map[string]interface{})
	for _, m := range ms {
		for k, v := range m {
			res[k] = v
		}
	}
	return res
}

func sha256sum(data string) string {
	bs := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", bs)
}
func (w *Wattpilot) getRequestId() int {
	current := w._requestId
	w._requestId += 1
	return current
}

func (w *Wattpilot) onEventHello(connection *websocket.Conn, message map[string]interface{}) {

	if hasKey(message, "hostname") {
		w._hostname = message["hostname"].(string)
	}
	if hasKey(message, "friendly_name") {
		w._name = message["friendly_name"].(string)
	} else {
		w._name = w._hostname
	}
	w._serial = message["serial"].(string)
	if hasKey(message, "version") {
		w._version = message["version"].(string)
	}
	w._manufacturer = message["manufacturer"].(string)
	w._devicetype = message["devicetype"].(string)
	w._protocol = message["protocol"].(float64)
	if hasKey(message, "secured") {
		w._secured = message["secured"].(bool)
	}

	log.Printf("Connected to WattPilot %s, Serial %s", w._name, w._serial)

	pwd_data := pbkdf2.Key([]byte(w._password), []byte(w._serial), 100000, 256, sha512.New)
	w._hashedpassword = base64.StdEncoding.EncodeToString([]byte(pwd_data))[:32]
}

func randomHexString(n int) string {
	b := make([]byte, (n+2)/2) // can be simplified to n/2 if n is always even

	if _, err := src.Read(b); err != nil {
		panic(err)
	}

	return hex.EncodeToString(b)[1 : n+1]
}

func (w *Wattpilot) onEventAuthRequired(connection *websocket.Conn, message map[string]interface{}) {

	token1 := message["token1"].(string)
	token2 := message["token2"].(string)

	w._token3 = randomHexString(32)
	hash1 := sha256sum(token1 + w._hashedpassword)
	hash := sha256sum(w._token3 + token2 + hash1)
	response := map[string]interface{}{
		"type":   "auth",
		"token3": w._token3,
		"hash":   hash,
	}
	w.onSendRepsonse(connection, response)
}
func (w *Wattpilot) onSendRepsonse(connection *websocket.Conn, message map[string]interface{}) {

	// if _secured {
	// 	msgId := message["requestId"].(string)
	// 	payload,_ := json.Marshal(message)

	// 	mac := hmac.New(sha256.New, []byte(_hashedpassword))
	// 	mac.Write(payload)
	// 	message = make(map[string]interface{})
	// 	message["type"] = "securedMsg"
	// 	message["data"] = string(payload)
	// 	message["requestId"] = msgId + "sm"
	// 	message["hmac"] = mac.Sum(nil)

	// }

	data, _ := json.Marshal(message)
	log.Println(string(data))
	err := connection.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		log.Println("Error during writing to websocket:", err)
		return
	}
}

func (w *Wattpilot) onEventResponse(connection *websocket.Conn, message map[string]interface{}) {
}
func (w *Wattpilot) onEventAuthSuccess(connection *websocket.Conn, message map[string]interface{}) {
	log.Println("Connected!")
	w.connected <- true
}
func (w *Wattpilot) onEventAuthError(connection *websocket.Conn, message map[string]interface{}) {
	log.Println("Authentication error")
	w.connected <- false
}
func (w *Wattpilot) onEventFullStatus(connection *websocket.Conn, message map[string]interface{}) {

	isPartial := message["partial"].(bool)
	status := message["status"].(map[string]interface{})

	w._status = merge(w._status, status)

	if isPartial {
		return
	}
	w.initialized <- true
	w._isInitialized = true
}
func (w *Wattpilot) onEventDeltaStatus(connection *websocket.Conn, message map[string]interface{}) {
	status := message["status"].(map[string]interface{})
	w._status = merge(w._status, status)
}
func (w *Wattpilot) onEventClearInverters(connection *websocket.Conn, message map[string]interface{}) {
	log.Println(message)
}
func (w *Wattpilot) onEventUpdateInverter(connection *websocket.Conn, message map[string]interface{}) {
	log.Println(message)
}

func (w *Wattpilot) Connect() {
	done = make(chan interface{})    // Channel to indicate that the receiverHandler is done
	interrupt = make(chan os.Signal) // Channel to listen for interrupt signal to terminate gracefully

	signal.Notify(interrupt, os.Interrupt) // Notify the interrupt channel for SIGINT

	socketUrl := "ws://" + w._host + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(socketUrl, nil)
	if err != nil {
		log.Fatal("Error connecting to Websocket Server:", err)
	}

	defer conn.Close()
	go w.receiveHandler(conn)
	w._currentConnection = conn
	go w.loop(conn)

	<-w.connected

	log.Println("Waiting for configuration...")

	<- w.initialized
}

func (w *Wattpilot) loop(conn *websocket.Conn) {

	for {
		select {
		// case <-time.After(time.Duration(1) * time.Millisecond * 1000):
		//     // Send an echo packet every second
		//     // err := conn.WriteMessage(websocket.TextMessage, []byte("Hello from GolangDocs!"))
		//     if err != nil {
		//         log.Println("Error during writing to websocket:", err)
		//         return
		//     }

		case <-interrupt:
			// We received a SIGINT (Ctrl + C). Terminate gracefully...
			log.Println("Received SIGINT interrupt signal. Closing all pending connections")
			// Close our websocket connection
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("Error during closing websocket:", err)
				return
			}

			select {
			case <-done:
				log.Println("Receiver Channel Closed! Exiting....")
			case <-time.After(time.Duration(1) * time.Second):
				log.Println("Timeout in closing receiving channel. Exiting....")
			}
			return
		}
	}
}

func (w *Wattpilot) receiveHandler(connection *websocket.Conn) {
	defer close(done)
	for {
		_, msg, err := connection.ReadMessage()
		if err != nil {
			log.Println("Error in receive:", err)
			return
		}
		log.Printf("Received: %s\n", msg)
		data := make(map[string]interface{})
		json.Unmarshal(msg, &data)
		msgType, isTypeAvailable := data["type"]
		if !isTypeAvailable {
			continue
		}
		funcCall, isKnown := w.eventHandler[msgType.(string)]
		if !isKnown {
			continue
		}
		log.Printf("Calling " + msgType.(string))
		funcCall(connection, data)
	}
}

func (w *Wattpilot) GetProperty(name string) (interface{}, error) {
	if !w._isInitialized {
		return nil, errors.New("Connection is not valid")
	}
	if !hasKey(w._status, name) {
		return nil, errors.New("Could not find " + name)
	}
	return w._status[name], nil
}
func (w *Wattpilot) SetProperty(name string, value interface{}) error {
	if !w._isInitialized {
		return errors.New("Connection is not valid")
	}
	if !hasKey(w._status, name) {
		return errors.New("Could not find " + name)
	}

	err := w.sendUpdate(name, value)
	if err != nil {
		return err
	}
	w._status[name] = value
	return nil
}
func (w *Wattpilot) sendUpdate(name string, value interface{}) error {
	message := make(map[string]interface{})
	message["type"] = "setValue"
	message["requestId"] = w.getRequestId()
	message["key"] = name
	message["value"] = value
	w.onSendRepsonse(w._currentConnection, message)
	return nil
}

func (w *Wattpilot) Status() (map[string]interface{},error) {
	if !w._isInitialized {
		return nil, errors.New("Connection is not initialzed")
	}
	return w._status, nil
}
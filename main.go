package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/gordonklaus/portaudio"
	"github.com/gorilla/websocket"
	"github.com/hajimehoshi/oto"
	"github.com/maxhawkins/go-webrtcvad"
)

const (
	sampleRate     = 16000
	channels       = 1
	bufferSize     = 320
	sampleBits     = 16
	bytesPerSample = 2
	websocketURI   = "ws://localhost:8001/ws"
)

var client = NewOllamaClient("http://192.168.25.63:11434")

func main() {
	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Fatal(err)
	}
	defer portaudio.Terminate()

	in := make([]int16, 320)
	stream, err := portaudio.OpenDefaultStream(channels, 0, float64(sampleRate), len(in), in)
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	// Initialize Oto
	ctx, err := oto.NewContext(8000, 1, 2, 640)

	if err != nil {
		log.Fatal(err)
	}

	player := ctx.NewPlayer()
	defer player.Close()

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	fmt.Println("Recording... Press Ctrl+C to stop")

	// Start the stream
	if err := stream.Start(); err != nil {
		log.Fatal(err)
	}
	defer stream.Stop()

	var silenceThreshold = 5
	var inputAudioBuffer [][]float32
	var silenceCount int

	vad, err := webrtcvad.New()
	if err != nil {
		log.Fatal(err)
	}

	if err := vad.SetMode(3); err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			// Read data from the microphone into the buffer
			if err := stream.Read(); err != nil {
				log.Println("Error reading from microphone:", err)
				break
			}
			data := int16ToByte(in)
			// Write the buffer data to the player
			floatArray, err := pcmToFloat32Array(data)
			if err != nil {
				log.Println("error converting pcm to float32:", err)
				continue
			}

			if active, err := vad.Process(16000, data); err != nil {
				log.Println("Error processing VAD:", err)
			} else if active {
				inputAudioBuffer = append(inputAudioBuffer, floatArray)
				//	calculateAudioLength(inputAudioBuffer, 16000)
				silenceCount = 0
			} else {
				silenceCount++
				if silenceCount > silenceThreshold {
					if len(inputAudioBuffer) > 0 {
						log.Println("Processing complete sentence")
						handleInputAudio(inputAudioBuffer, player)
						inputAudioBuffer = nil // Reset buffer
					}
				}
			}

			// if _, err := player.Write(data); err != nil {
			// 	log.Println("Error writing to speaker:", err)
			// 	break
			// }
		}
	}()

	<-done
	fmt.Println("Stopping...")

	time.Sleep(time.Second)
}
func handleInputAudio(buffer [][]float32, player *oto.Player) {
	var mergedBuffer []float32
	for _, data := range buffer {
		mergedBuffer = append(mergedBuffer, data...)
	}
	length := calculateAudioLength(buffer, 16000)
	log.Println("Audio length:", length)
	if length < 0.40 {
		log.Println("Audio length is less than 0.45 seconds, skipping processing.")
		return
	}
	transcription, err := sendFloat32ArrayToServer("http://localhost:8002/complete_transcribe_r", mergedBuffer)
	if err != nil {
		log.Println("Error sending data to server:", err)
		return
	}
	excludedWords := []string{"Продолжение следует...", "Субтитры сделал DimaTorzok", "Субтитры создавал DimaTorzok"}
	for _, word := range excludedWords {
		if transcription == word {
			log.Println("Transcription contains excluded word, stopping further processing.")
			return
		}
	}
	res, errO := client.GenerateNoStream("gemma2:9b", transcription, nil, "")
	if errO != nil {
		fmt.Println("Error:", err)
	}
	log.Println("Received result:", res["response"])
	data := map[string]interface{}{
		"message":  res["response"],
		"language": "ru",
		"speed":    1.0,
	}
	log.Println("Using transcription:", transcription)
	websocketSendReceive(websocketURI, data, player)

}

func int16ToByte(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, sample := range samples {
		buf[2*i] = byte(sample)
		buf[2*i+1] = byte(sample >> 8)
	}
	return buf
}
func calculateAudioLength(inputAudioBuffer [][]float32, sampleRate int) float64 {
	totalSamples := 0
	for _, buffer := range inputAudioBuffer {
		totalSamples += len(buffer)
	}
	lengthInSeconds := float64(totalSamples) / float64(sampleRate)
	return lengthInSeconds
}
func pcmToFloat32Array(pcmData []byte) ([]float32, error) {
	if len(pcmData)%2 != 0 {
		return nil, fmt.Errorf("pcm data length must be even")
	}
	float32Array := make([]float32, len(pcmData)/2)
	buf := bytes.NewReader(pcmData)
	for i := 0; i < len(float32Array); i++ {
		var sample int16
		if err := binary.Read(buf, binary.LittleEndian, &sample); err != nil {
			return nil, fmt.Errorf("failed to read sample: %v", err)
		}
		float32Array[i] = float32(sample) / 32768.0
	}
	return float32Array, nil
}

func sendFloat32ArrayToServer(serverAddress string, float32Array []float32) (string, error) {
	var buf bytes.Buffer

	for _, f := range float32Array {
		if err := binary.Write(&buf, binary.LittleEndian, f); err != nil {
			return "", fmt.Errorf("failed to write float32: %v", err)
		}
	}

	req, err := http.NewRequest("POST", serverAddress, &buf)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	log.Println("Server response:", string(body))
	//returh response ["transcription": message]
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("error unmarshalling response body: %v", err)
	}

	if transcription, ok := result["transcription"].(string); ok {
		log.Println("Transcription:", transcription)
		return transcription, nil
		//return transcription
	} else {
		return "", fmt.Errorf("transcription not found in response")
	}
}

func websocketSendReceive(uri string, data map[string]interface{}, player *oto.Player) {
	wsConn, _, err := websocket.DefaultDialer.Dial(uri, nil)
	if err != nil {
		log.Println("Failed to connect to WebSocket:", err)
		return
	}
	defer wsConn.Close()

	err = wsConn.WriteJSON(data)
	if err != nil {
		log.Println("Failed to send JSON:", err)
		return
	}

	for {
		_, message, err := wsConn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Unexpected WebSocket closure: %v", err)
			}
			break
		}

		var jsonMessage map[string]interface{}
		if err := json.Unmarshal(message, &jsonMessage); err == nil {
			if typeField, ok := jsonMessage["type"].(string); ok && typeField == "end_of_audio" {
				log.Println("End of conversation")
				break
			}
			log.Println("Received message:", jsonMessage)
		} else {
			if _, err := player.Write(message); err != nil {
				log.Println("Error writing to speaker:", err)
				break
			}
			// Assume non-JSON data can be audio bytes

			// if _, err := conn.Write(audiosocket.SlinMessage(message)); err != nil {
			// 	log.Println("Error writing to connection:", err)
			// 	break
			// }
		}
	}
}

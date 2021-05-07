// Package gst provides an easy API to create an appsink pipeline
package gst

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-app-1.0
#include "gst.h"
*/
import "C"
import (
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/creamlab/webrtc-transform/helpers"
	"github.com/pion/webrtc/v3"
)

var vp8ProcPipeline string
var vp8RawPipeline string
var opusProcPipeline string
var opusRawPipeline string

func init() {
	vp8ProcPipeline = helpers.ReadConfig("vp8-norec")
	vp8RawPipeline = helpers.ReadConfig("vp8-norec")
	opusProcPipeline = helpers.ReadConfig("opus-norec")
	opusRawPipeline = helpers.ReadConfig("opus-norec")
}

func StartMainLoop() {
	C.gstreamer_send_start_mainloop()
}

// Pipeline is a wrapper for a GStreamer Pipeline
type Pipeline struct {
	Pipeline   *C.GstElement
	track      *webrtc.TrackLocalStaticRTP
	id         int
	filePrefix string
}

var pipelines = make(map[int]*Pipeline)
var pipelinesLock sync.Mutex

func randomEffect() string {
	rand.Seed(time.Now().Unix())
	// options := []string{
	// 	"rippletv", "dicetv", "edgetv", "optv", "quarktv", "radioactv", "warptv", "shagadelictv", "streaktv", "vertigotv",
	// }
	options := []string{"identity"}
	return options[rand.Intn(len(options))]
}

func newPipelineStr(filePrefix string, codecName string, proc bool) (pipelineStr string) {
	codecName = strings.ToLower(codecName)

	switch codecName {
	case "vp8":
		if proc {
			pipelineStr = vp8ProcPipeline
		} else {
			pipelineStr = vp8RawPipeline
		}
	case "opus":
		if proc {
			pipelineStr = opusProcPipeline
		} else {
			pipelineStr = opusRawPipeline
		}
	default:
		panic("Unhandled codec " + codecName)
	}
	pipelineStr = strings.Replace(pipelineStr, "${prefix}", filePrefix, -1)
	return
}

// CreatePipeline creates a GStreamer Pipeline
func CreatePipeline(filePrefix string, codecName string, proc bool, track *webrtc.TrackLocalStaticRTP) *Pipeline {
	pipelineStr := newPipelineStr(filePrefix, codecName, proc)

	pipelineStrUnsafe := C.CString(pipelineStr)
	defer C.free(unsafe.Pointer(pipelineStrUnsafe))

	pipelinesLock.Lock()
	defer pipelinesLock.Unlock()

	pipeline := &Pipeline{
		Pipeline:   C.gstreamer_send_create_pipeline(pipelineStrUnsafe),
		track:      track,
		id:         len(pipelines),
		filePrefix: filePrefix,
	}

	pipelines[pipeline.id] = pipeline
	return pipeline
}

// Start starts the GStreamer Pipeline
func (p *Pipeline) Start() {
	log.Printf("[gst] pipeline started: %d %s\n", p.id, p.filePrefix)
	C.gstreamer_send_start_pipeline(p.Pipeline, C.int(p.id))
}

// Stop stops the GStreamer Pipeline
func (p *Pipeline) Stop() {
	log.Printf("[gst] pipeline stopped: %d %s\n", p.id, p.filePrefix)
	C.gstreamer_send_stop_pipeline(p.Pipeline)
}

//export goHandlePipelineBuffer
func goHandlePipelineBuffer(buffer unsafe.Pointer, bufferLen C.int, duration C.int, pipelineID C.int) {
	pipelinesLock.Lock()
	pipeline, ok := pipelines[int(pipelineID)]
	pipelinesLock.Unlock()

	if ok {
		if _, err := pipeline.track.Write(C.GoBytes(buffer, bufferLen)); err != nil {
			panic(err)
		}
	} else {
		log.Printf("discarding buffer, no pipeline with id %d", int(pipelineID))
	}
	C.free(buffer)
}

// Push pushes a buffer on the appsrc of the GStreamer Pipeline
func (p *Pipeline) Push(buffer []byte) {
	b := C.CBytes(buffer)
	defer C.free(b)
	C.gstreamer_send_push_buffer(p.Pipeline, b, C.int(len(buffer)))
}

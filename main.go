package main

import (
	"log"
	"os"

	"github.com/creamlab/ducksoup/front"
	"github.com/creamlab/ducksoup/gst"
	"github.com/creamlab/ducksoup/helpers"
	"github.com/creamlab/ducksoup/server"
)

var (
	cmdBuildMode bool = false
)

func init() {

	if os.Getenv("DS_ENV") == "BUILD_FRONT" {
		cmdBuildMode = true
	}

	helpers.EnsureDir("./data")

	// init logging
	log.SetFlags(log.Lmicroseconds)
	log.SetOutput(os.Stdout)
}

func main() {
	// always build front (in watch mode or not, depending on DS_ENV value, see front/build.go)
	front.Build()

	// run ducksoup only if not in BUILD_FRONT DS_ENV
	if !cmdBuildMode {
		defer func() {
			log.Println("[main] stopped")
			if r := recover(); r != nil {
				log.Println("[recov] main has recovered: ", r)
			}
		}()

		// launch http (with websockets) server
		go server.ListenAndServe()

		// start Glib main loop for GStreamer
		gst.StartMainLoop()
	}
}

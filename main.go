package main

import (
	"fmt"         //output
	"image"       //image
	"image/color" //color
	"image/draw"  //draw
	_ "image/jpeg"
	_ "image/png"
	"io/ioutil" //decode fonts
	"log"
	"math"
	"math/rand"
	"net/http" //get image from url
	"os"       //get user
	"time"

	"github.com/cenkalti/dominantcolor" //find dominant color of image
	"github.com/disintegration/imaging"

	//image transformation
	colorconvert "github.com/gilphilbert/gocolor"   //color conversion
	framebuffer "github.com/gilphilbert/go-framebuffer" //write image to fb0
	"github.com/golang/freetype"                    //import fonts
	gosocketio "github.com/graarh/golang-socketio"  //socketio server/client (for us, the client)
	"github.com/graarh/golang-socketio/transport"
	"github.com/stianeikeland/go-rpio" //gpio management to turn backlight on/off
	"golang.org/x/image/font"          //font library for hinting
)

//now we need to add GPIO and turn off the screen when we're not using it............

//used to pass socketio data
type Message struct {
	Status     string `json:"status"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	Albumart   string `json:"albumart"`
	TrackType  string `json:"trackType"`
	Seek       int    `json:"seek"`
	Duration   int    `json:"duration"`
	SampleRate string `json:"samplerate"`
	BitDepth   string `json:"bitdepth"`
}

//details of the screen
type screen struct {
	Width          int
	Height         int
	HPad           int
	VPad           int
	ProgressHeight int
	ProgressTop    int
	TitleSize      int
	TextSize       int
}

//draw options (so we can choose what to send)
type drawOpts struct {
	Fb      *framebuffer.Framebuffer
	Message Message
	NewBase bool
}

//details of the current background image
type builtImage struct {
	image     *image.RGBA
	base      color.RGBA
	lighter   color.RGBA
	lightness float64
	color     color.RGBA
}

//variables (used for multiple threads)
var (
	fontFile      = "./sen.ttf"    //the font to use
	ticking       = false        //whether the clock is ticking (increments playback)
	display       = screen{}     //details of the display
	baseImage     = builtImage{} //the current background image
	playing       = Message{}    //current state
	rpi           = false        //is this a raspberry pi?
	backlight     = rpio.Pin(0)  //pin the backlight is attached to
	screenTimeout = -1           //screen timeout, used to count down and turn screen off
	connected     = false
)

/*
converts an integer in milliseconds into a string formatted as [H:][M]M:SS
*/
func getTimeStringMilliseconds(tm int) string {
	//move to top of progress bar and print seek time
	seek := time.Duration(tm) * time.Millisecond
	hours := int(math.Floor(seek.Hours()))
	if hours > 0 {
		seek = seek - (time.Duration(hours) * time.Hour)
	}
	minutes := int(math.Floor(seek.Minutes()))
	if minutes > 0 {
		seek = seek - (time.Duration(minutes) * time.Minute)
	}
	seconds := int(math.Floor(seek.Seconds()))
	seekString := fmt.Sprintf("%d:%02d", minutes, seconds)
	if hours > 0 {
		seekString = fmt.Sprintf("%d:", hours) + seekString
	}
	return seekString
}

/*
generates the alpha layer over the image. Right now, it only gradients top left to bottom right (1 at top left, 0 at bottom right)
--> takes x and y (current position on screen) and height and width and screen all as integers
--> returns a uint8 (0-255) of the opacity (0 being transparent, 255 being opaque)
*/
func gradientAlpha(x, y, disp_w, disp_h int) uint8 {
	alpha_x := uint8((1.0 - (float64(x) / float64(disp_w))) * 127.5)
	alpha_y := uint8((1.0 - (float64(y) / float64(disp_h))) * 127.5)
	return (alpha_x + alpha_y)
}

func loadFont(surface draw.Image) *freetype.Context {
	fontBytes, err := ioutil.ReadFile(fontFile)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	f, err := freetype.ParseFont(fontBytes)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(f)
	c.SetClip(surface.Bounds())
	c.SetDst(surface)
	c.SetHinting(font.HintingFull)
	return c
}

/*
builds the base image from the albumart and generates the color palete
needs an albumart url, sizes to the current screen (assumes horizontal)
*/
func buildBase(aaurl string) {
	log.Println(aaurl)
	url := "http://192.168.68.110:3000" + aaurl

	//------ load albumart ------//

	//fetch the albumart from the server
	res, err := http.Get(url)
	if err != nil || res.StatusCode != 200 {
		// handle errors
		fmt.Println(err)
		os.Exit(1)
	}
	defer res.Body.Close()

	//decode the albumart
	img, _, err := image.Decode(res.Body)
	if err != nil {
		// handle error
		log.Println(err)
		os.Exit(1)
	}

	//resize the image to the correct width and crop to the correct height (this ensures we get cover)
	img = imaging.Resize(img, display.Width, 0, imaging.Lanczos)
	img = imaging.CropAnchor(img, display.Width, display.Height, imaging.Center)

	//------ define the colors we'll need ------//

	//get the base color, this is the dominant color from the image. NOT the average color (which can be very different)
	baseImage.base = dominantcolor.Find(img)
	//get the lightness of the base color
	baseImage.lightness = colorconvert.Lightness(baseImage.base)
	//generate a lighter version of the base color
	baseImage.lighter = colorconvert.LightenRGBA(baseImage.base, 40)
	//color of anything we draw on the overlay will ne white...
	baseImage.color = color.RGBA{255, 255, 255, 255}
	//unless the lightness is over 50%, in which case we'll use black
	if baseImage.lightness >= 127 {
		baseImage.color = color.RGBA{0, 0, 0, 0}
	}

	//------ build overlay ------//

	//create overlay image
	overlay := image.NewNRGBA(image.Rect(0, 0, display.Width, display.Height))
	//white := color.RGBA{0, 0, 0, 128}
	//draw.Draw(overlay, overlay.Bounds(), &image.Uniform{white}, image.ZP, draw.Src)

	for x := 0; x < display.Width; x++ {
		for y := 0; y < display.Height; y++ {
			alpha := gradientAlpha(x, y, display.Width, display.Height)
			c := color.NRGBA{baseImage.base.R, baseImage.base.G, baseImage.base.B, alpha}
			overlay.SetNRGBA(x, y, c)
		}
	}

	baseImage.image = image.NewRGBA(img.Bounds())
	draw.Draw(baseImage.image, img.Bounds(), img, image.ZP, draw.Src)

	//if the location is /albumart then we're seeing the default albumart which means the queue is empty
	if aaurl != "/albumart" {
		draw.DrawMask(baseImage.image, img.Bounds(), overlay, image.ZP, nil, image.ZP, draw.Over)
	}
}

/*
creates the total image, including the background (buildBase) and text/drawings on it combined
and writes it to the framebuffer provided
*/
func drawScreen(o drawOpts) {
	data := o.Message
	fb := o.Fb

	overlay := image.NewNRGBA(image.Rect(0, 0, display.Width, display.Height))
	//------ add text ------//

	if data.Title != "" {
		//load the font
		textWriter := loadFont(overlay)
		//set the color for the overlay
		textWriter.SetSrc(image.NewUniform(baseImage.color))
		//set our top-left position (based on screen padding)
		pt := freetype.Pt(display.HPad, display.VPad)

		//draw the title, set the size first
		textWriter.SetFontSize(float64(display.TitleSize))
		//the ndraw the title
		textWriter.DrawString(data.Title, pt)

		//set the font size for the rest of the text
		textWriter.SetFontSize(float64(display.TextSize))

		//add a space and insert artist
		pt.Y += textWriter.PointToFixed(float64(display.TextSize) * 1.5)
		textWriter.DrawString(data.Artist, pt)

		//add a space and insert album
		pt.Y += textWriter.PointToFixed(float64(display.TextSize) * 1)
		textWriter.DrawString(data.Album, pt)

		//add a space and insert quality
		pt.Y += textWriter.PointToFixed(float64(display.TextSize) * 1.5)
		textWriter.SetSrc(image.NewUniform(baseImage.lighter))
		textWriter.DrawString(data.SampleRate+" | "+data.BitDepth, pt)

		/*
			------ PROGRESS BAR ------
		*/

		pt.Y = textWriter.PointToFixed(float64(display.ProgressTop - int(math.Floor(float64(display.TextSize)/2.0))))
		textWriter.SetSrc(image.NewUniform(baseImage.color))
		textWriter.DrawString(getTimeStringMilliseconds(data.Seek)+" / "+getTimeStringMilliseconds(data.Duration*1000), pt)

		//draw the underlying bar
		for y := display.ProgressTop; y < (display.ProgressTop + display.ProgressHeight); y++ {
			for x := display.HPad; x < (display.Width - display.HPad); x++ {
				overlay.Set(x, y, baseImage.base)
			}
		}

		//get the length of the progress based on play so far	for y := display.ProgressTop; y < (display.ProgressTop + display.ProgressHeight); y++ {
		progress := float64(data.Seek/1000) / float64(data.Duration)
		progressBar := int(float64(display.Width-(display.HPad*2)) * progress)
		//draw the actual progress
		for y := display.ProgressTop; y < (display.ProgressTop + display.ProgressHeight); y++ {
			for x := display.HPad; x < progressBar; x++ {
				overlay.Set(x, y, baseImage.color)
			}
		}
	}

	overlay = imaging.Rotate180(overlay)

	//combine the albumart image and overlays
	final := image.NewRGBA(baseImage.image.Bounds())
	draw.Draw(final, baseImage.image.Bounds(), baseImage.image, image.ZP, draw.Src)
	draw.DrawMask(final, final.Bounds(), overlay, image.ZP, nil, image.ZP, draw.Over)

	//------ output ------//

	///write to the framebuffer
	fb.DrawImage(0, 0, final)

	//if this is a raspberry pi, turn on the backlight
	if rpi == true {
		backlight.High()
	}

	if data.Status == "play" {
		ticking = true
	} else {
		ticking = false
	}

}

/*
generates the display details from the provided framebuffer
*/
func configureScreen(fb *framebuffer.Framebuffer) {
	display.Width = fb.Xres
	display.Height = fb.Yres

	display.VPad = int(float64(display.Height) * 0.15625)
	display.HPad = int(float64(display.Width) * 0.0625)

	display.TitleSize = int(float64(display.Height) * .1)
	display.TextSize = int(float64(display.Height) * .078125)

	display.ProgressHeight = int(float64(display.Height) * 0.00625)
	display.ProgressTop = int(float64(display.Height) * 0.8)
}

/*
main thread, opens sockets, gpio (backlight control), sets up the screen and generates the triggers
also counts seconds and turns of the screen when there's no activity
*/
func main() {
	err := rpio.Open()
	if err != nil {
		log.Println("Can't access GPIO, assuming we're not an a RPi")
		rpi = false
	} else {
		rpi = true
		backlight = rpio.Pin(22)
		backlight.Output()
	}

	socket, err := gosocketio.Dial(
		gosocketio.GetUrl("localhost", 3000, false),
		transport.GetDefaultWebsocketTransport(),
	)
	defer socket.Close()

	//create the framebuffer
	fb, err := framebuffer.Open("fb0")
	if err != nil {
		log.Println("Can't open frambuffer, are you a member of video?")
		os.Exit(1)
	}
	defer fb.Release()

	configureScreen(fb)

	socket.On("pushState", func(c *gosocketio.Channel, msg Message) string {
		//ignore erroneous states from volumio websocket server
		if (msg.TrackType == "flac" && msg.BitDepth == "") || (msg.Status == "play" && msg.Seek == 0) {
			return ""
		}
		if msg.Status == playing.Status && msg.Title == playing.Title && msg.Artist == playing.Artist && msg.Seek == playing.Seek {
			return ""
		}
		//volumio sends lots of nonsense we want to skip past - duplicates, etc. so let's wait to help avoid a collision
		time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)
		if msg == playing {
			return ""
		}
		//the state has changed (hopefully...)
		ticking = false
		if msg.Albumart != playing.Albumart {
			//we need to generate new art since the albumart has changed
			buildBase(msg.Albumart)
		}
		log.Println(msg)

		//store the new data
		playing = msg
		//if we've got this far and there's no baseImage (i.e., we've seen this state but the image generation isn't complete) then another thread is running
		if baseImage.image == nil {
			return ""
		}
		//draw the screen
		drawScreen(drawOpts{Fb: fb, Message: playing})
		//if the status is not play
		if msg.Status != "play" {
			screenTimeout = 60
		} else {
			screenTimeout = -1
		}
		return "result"
	})
	socket.On(gosocketio.OnConnection, func(c *gosocketio.Channel) {
		log.Println("Connected to server")
		connected = true
		socket.Emit("getState", "")
	})
	socket.On(gosocketio.OnDisconnection, func(c *gosocketio.Channel) {
		log.Println("Disconnected from server, trying to reconnect")
		//gosocketio.Redial(socket)
		ticking = false
		//connected = false
	})

	for {
		if ticking == true {
			//wait the predefined amount of time
			playing.Seek = playing.Seek + 1000
			drawScreen(drawOpts{Fb: fb, Message: playing})
		}
		//turn off the screen if we've timed out...
		if screenTimeout >= 0 {
			screenTimeout = screenTimeout - 1
			if screenTimeout == 0 && rpi == true {
				backlight.Low()
			}
		}
		//log.Println(socket)
		//if connected == false {
		//	log.Println("Trying to reconnect...")

		//	socket, err = gosocketio.Dial(
		//		gosocketio.GetUrl("192.168.68.110", 3000, false),
		//		transport.GetDefaultWebsocketTransport(),
		//	)
		//defer socket.Close()
		//	break
		//}
		time.Sleep(time.Duration(1000) * time.Millisecond)
	}
	//select {}
}

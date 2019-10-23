package main

import (
	"fmt"         //output
	"image"       //image
	"image/color" //color
	"image/draw"  //draw
	_ "image/jpeg"
	"log"

	"github.com/gilphilbert/gocolor" //color conversion
	"github.com/gilphilbert/goframebuf"  //write image to fb0
	"github.com/graarh/golang-socketio/transport"

	//if the input is jpeg
	_ "image/png" //if the input is png

	"io/ioutil" //decode fonts
	"net/http"  //get image from url
	"os"        //get user

	"github.com/cenkalti/dominantcolor"            //find dominant color of image
	"github.com/disintegration/imaging"            //image transformation
	"github.com/golang/freetype"                   //import fonts
	gosocketio "github.com/graarh/golang-socketio" //socketio server/client (for us, the client)
	"golang.org/x/image/font"                      //font library for hinting
)

var (
	fontFile = "sen.ttf"
	textSize = 25
)

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

func drawScreen(fb *framebuffer.Framebuffer) {
	//hardcode some variables for now...
	progress := 0.66
	url := "https://musicrow.com/wp-content/uploads/2018/08/The-Eagles-Their-Greatest-Hits-vk-.jpg"

	//set the dimensions of the screen from the framebuffer
	disp_w := fb.Xres
	disp_h := fb.Yres

	//calculate padding values
	padd_tb := int(float64(disp_h) * 0.15625)
	padd_lr := int(float64(disp_w) * 0.0625)

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
	img = imaging.Resize(img, disp_w, 0, imaging.Lanczos)
	img = imaging.CropAnchor(img, disp_w, disp_h, imaging.Center)

	//------ define the colors we'll need ------//

	//get the base color, this is the dominant color from the image. NOT the average color (which can be very different)
	baseColor := dominantcolor.Find(img)
	//get the lightness of the base color
	baseLightness := colorconvert.Lightness(baseColor)
	//generate a lighter version of the base color
	lighterBase := colorconvert.LightenRGBA(baseColor, 40)
	//color of anything we draw on the overlay will ne white...
	overlayColor := color.RGBA{255, 255, 255, 255}
	//unless the lightness is over 50%, in which case we'll use black
	if baseLightness >= 127 {
		overlayColor = color.RGBA{0, 0, 0, 0}
	}

	//------ build overlay ------//

	//create overlay image
	overlay := image.NewNRGBA(image.Rect(0, 0, disp_w, disp_h))
	for x := 0; x < disp_w; x++ {
		for y := 0; y < disp_h; y++ {
			alpha := gradientAlpha(x, y, disp_w, disp_h)
			c := color.NRGBA{baseColor.R, baseColor.G, baseColor.B, alpha}
			overlay.SetNRGBA(x, y, c)
		}
	}

	//font definitions (sizes)
	titleSize := int(float64(disp_h) * .1)
	textSize := int(float64(disp_h) * .078125)

	//load the font
	textWriter := loadFont(overlay)
	//set the color for the overlay
	textWriter.SetSrc(image.NewUniform(overlayColor))
	//set our top-left position (based on screen padding)
	pt := freetype.Pt(padd_lr, padd_tb)

	//draw the title, set the size first
	textWriter.SetFontSize(float64(titleSize))
	//the ndraw the title
	textWriter.DrawString("One Of These Nights", pt)

	//set the font size for the rest of the text
	textWriter.SetFontSize(float64(textSize))

	//add a space and insert artist
	pt.Y += textWriter.PointToFixed(float64(titleSize) * 1.5)
	textWriter.DrawString("Eagles", pt)

	//add a space and insert album
	pt.Y += textWriter.PointToFixed(float64(titleSize) * 1)
	textWriter.DrawString("One Of These Nights", pt)

	//add a space and insert quality
	pt.Y += textWriter.PointToFixed(float64(titleSize) * 1.5)
	textWriter.SetSrc(image.NewUniform(lighterBase))
	textWriter.DrawString("192kHz 24-bit", pt)

	//get line width:
	lineWidth := int(float64(disp_h) * 0.00625)
	//draw the total progress bar
	progress_y := int(float64(disp_h) * 0.8)
	for y := progress_y; y < (progress_y + lineWidth); y++ {
		for x := padd_lr; x < (disp_w - padd_lr); x++ {
			overlay.Set(x, y, lighterBase)
		}
	}
	//get the length of the progress based on play so far
	progressBar := int(float64(disp_w-padd_lr) * progress)
	//draw the actual progress
	for y := progress_y; y < (progress_y + lineWidth); y++ {
		for x := padd_lr; x < progressBar; x++ {
			overlay.Set(x, y, overlayColor)
		}
	}

	//------ show progress ------//

	//combine the albumart image and overlays
	final := image.NewRGBA(overlay.Bounds())
	draw.Draw(final, img.Bounds(), img, image.ZP, draw.Src)
	//mask := image.NewUniform(color.Alpha{255})
	draw.DrawMask(final, final.Bounds(), overlay, image.ZP, nil, image.ZP, draw.Over)

	//------ output ------//

	///write to the framebuffer
	fb.DrawImage(0, 0, final)
	//for now, save the image
	//of, _ := os.Create("screen.jpg")
	//defer of.Close()
	//jpeg.Encode(of, final, nil)

}

func main() {
	socket, err := gosocketio.Dial(
		gosocketio.GetUrl("192.168.68.110", 3000, false),
		transport.GetDefaultWebsocketTransport(),
	)
	defer socket.Close()

	type SocketData struct {
		Data string
	}

	//drawScreen(fb)
	socket.On("pushState", func(c *gosocketio.Channel, msg string) string {
		log.Println("Something successfully handled")
		fmt.Println(c)
		fmt.Println(msg)
		//you can return result of handler, in caller case
		//handler will be converted from "emit" to "ack"
		return "result"
	})
	socket.On(gosocketio.OnConnection, func(c *gosocketio.Channel) {
		log.Println("Connected to server")
		socket.Emit("getState", "")
	})

	//create the framebuffer
	fb, err := framebuffer.NewFramebuffer("fb0")
	if err != nil {
		log.Println("Can't open frambuffer, are you a member of video?")
		os.Exit(1)
	}
	defer fb.Release()

	for {
		ia := socket.Channel.IsAlive()
		if ia == false {
			break
		}
	}
}

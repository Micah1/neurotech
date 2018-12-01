package main

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"time"

	pubsub "github.com/dustin/go-broadcast"
	"github.com/mjibson/go-dsp/fft"
)

const (
	Set2ChanModeWithSampleRate500Hz = "X"
	Set2ChanModeWithSampleRate250Hz = "U"
	SetSampleRate500Hz              = "V"
	SetSampleRate250Hz              = "W"
	SetSampleRate190Hz              = "B"
	SetLowPass32Hz                  = "S"
	SetLowPass40Hz                  = "M"
	SetLowPass50Hz                  = "Z"
	SetLowPass100Hz                 = "Q"
	SetLowPass150Hz                 = "O"
	SetLowPass200Hz                 = "P"
	SetBypassFilter                 = "I"
)

var BrainduinoCommands map[string]struct{} = map[string]struct{}{
	Set2ChanModeWithSampleRate500Hz: struct{}{},
	Set2ChanModeWithSampleRate250Hz: struct{}{},
	SetSampleRate500Hz:              struct{}{},
	SetSampleRate250Hz:              struct{}{},
	SetSampleRate190Hz:              struct{}{},
	SetLowPass32Hz:                  struct{}{},
	SetLowPass40Hz:                  struct{}{},
	SetLowPass50Hz:                  struct{}{},
	SetLowPass100Hz:                 struct{}{},
	SetLowPass150Hz:                 struct{}{},
	SetLowPass200Hz:                 struct{}{},
	SetBypassFilter:                 struct{}{},
}

func isValidCommand(s string) bool {
	_, ok := BrainduinoCommands[s]
	return ok
}

type ListenerType int

const (
	SampleListener ListenerType = iota
	FFTListener
)

type Subscriber interface {
	Register(ListenerType, chan<- interface{})
	Unregister(ListenerType, chan<- interface{})
}

type Device interface {
	io.ReadWriteCloser
	Subscriber
}

type Sample struct {
	Name           string
	Channels       []float64
	Timestamp      time.Time
	SequenceNumber uint
}

type FFTData struct {
	Name           string
	Channels       [][]float64
	Timestamp      time.Time
	SequenceNumber uint
}

type Brainduino struct {
	io.ReadWriteCloser
	offsethighbit  int
	numchan        int
	wordsize       int
	rawBroadcaster pubsub.Broadcaster
	fftBroadcaster pubsub.Broadcaster
}

func (b *Brainduino) adcnorm(raw int) float64 {
	return float64(raw) * 5.0 / float64(b.offsethighbit)
}

func (b *Brainduino) offsetBinaryToInt(hexstr []byte) int {
	var x int

	buf := bytes.NewBuffer(hexstr)
	_, err := fmt.Fscanf(buf, "%x", &x)
	if err != nil {
		fmt.Printf("Error scanning buffer %v in offsetBinaryToInt: %s\n", hexstr, err)
		return int(b.offsethighbit)
	}

	if x&b.offsethighbit == 0 {
		x -= b.offsethighbit
	} else {
		x = x & ^b.offsethighbit
	}

	return x
}

func (b *Brainduino) fftloop() {
	// assumes b.numchan == 2
	ctr := 0
	var seqnum uint
	fftsize := 256
	fftdata0 := make([]float64, fftsize)
	fftdata1 := make([]float64, fftsize)
	rawlistener := make(chan interface{})
	b.rawBroadcaster.Register(rawlistener)
	for {
		s := <-rawlistener
		sample := s.(Sample)
		fftdata0[ctr%fftsize] = sample.Channels[0]
		fftdata1[ctr%fftsize] = sample.Channels[1]
		// Set the frequency that the FFT is sent out.
		// e.g. ctr%2==0, every other sample
		//      ctr%10==0, every 10th sample
		//      ctr%250==0, every 250th sample
		if ctr%4 == 0 {
			fftd := FFTData{
				Name:           "fft",
				Channels:       make([][]float64, b.numchan),
				SequenceNumber: seqnum,
				Timestamp:      time.Now(),
			}
			fftd.Channels[0] = abs(fft.FFTReal(fftdata0))[:125]
			fftd.Channels[1] = abs(fft.FFTReal(fftdata1))[:125]
			b.fftBroadcaster.Submit(fftd)
			seqnum++
		}
		ctr++

	}
}

func (b *Brainduino) readloop() {
	// assumes b.numchan == 2
	// works with 3 tabs per sample
	buf := make([]byte, 14)
	chans := make([][]byte, b.numchan)
	for i := 0; i < b.numchan; i++ {
		chans[i] = make([]byte, b.wordsize)
	}

	ctr := 0
	tabctr := 0
	incmdseq := false

	ts := time.Now()
	var seqnum uint
	for {
		n, err := b.Read(buf)
		if err != nil {
			fmt.Printf("Failed to read brainduino: %s\n", err)
			continue
		}
		ts = time.Now()
		for _, val := range buf[:n] {
			if !b.isdatabyte(val) {
				incmdseq = true
				continue
			}

			if incmdseq && val == '\x0D' {
				incmdseq = false
				continue
			}

			if val == '\r' {
				sample := Sample{
					Name:           "sample",
					Channels:       make([]float64, b.numchan),
					Timestamp:      ts,
					SequenceNumber: seqnum,
				}
				for i := 0; i < b.numchan; i++ {
					sample.Channels[i] = b.adcnorm(b.offsetBinaryToInt(chans[i]))
					chans[i] = []byte{'\x00', '\x00', '\x00', '\x00', '\x00', '\x00'}
				}
				b.rawBroadcaster.Submit(sample)
				seqnum++
				tabctr = 0
				ctr = 0
			} else if val == '\t' {
				tabctr++
			} else if (tabctr == 0 || tabctr == 1) && b.isdatabyte(val) {
				chans[0][ctr%6] = val
				ctr++
			} else if (tabctr == 2 || tabctr == 3) && b.isdatabyte(val) {
				chans[1][ctr%6] = val
				ctr++
			}
		}
	}
}

func (b *Brainduino) isdatabyte(bb byte) bool {
	return (bb == '\x09' ||
		bb == '\x0D' ||
		bb == '\x30' ||
		bb == '\x31' ||
		bb == '\x32' ||
		bb == '\x33' ||
		bb == '\x34' ||
		bb == '\x35' ||
		bb == '\x36' ||
		bb == '\x37' ||
		bb == '\x38' ||
		bb == '\x39' ||
		bb == '\x41' ||
		bb == '\x42' ||
		bb == '\x43' ||
		bb == '\x44' ||
		bb == '\x45' ||
		bb == '\x46')
}

func (b Brainduino) Register(t ListenerType, listener chan<- interface{}) {
	switch t {
	case SampleListener:
		b.rawBroadcaster.Register(listener)
	case FFTListener:
		b.fftBroadcaster.Register(listener)
	}
}

func (b Brainduino) Unregister(t ListenerType, listener chan<- interface{}) {
	switch t {
	case SampleListener:
		b.rawBroadcaster.Unregister(listener)
	case FFTListener:
		b.fftBroadcaster.Unregister(listener)
	}
}

func NewBrainduino(device io.ReadWriteCloser) Device {
	brainduino := Brainduino{
		ReadWriteCloser: device,
		offsethighbit:   1 << 23,
		wordsize:        6,
		numchan:         2,
		rawBroadcaster:  pubsub.NewBroadcaster(0),
		fftBroadcaster:  pubsub.NewBroadcaster(0),
	}
	go brainduino.readloop()
	go brainduino.fftloop()
	return brainduino
}

type mockDevice struct {
	datastream chan byte
}

func randomDatastream(c chan byte) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	buf := make([]byte, 64)
	ctr := 0
	for {
		n, err := rnd.Read(buf)
		if err != nil {
			fmt.Printf("Error reading %d bytes from buf %v: %s\n", n, buf, err)
		}
		hexstr := []byte(fmt.Sprintf("%X", buf))
		for _, v := range hexstr {
			if ctr == 6 {
				c <- '\t'
				ctr++
			} else if ctr == 13 {
				c <- '\r'
				ctr = 0
				time.Sleep(time.Millisecond * 4)
			}
			c <- v
			ctr++
		}
	}
}

func (md mockDevice) Read(buf []byte) (int, error) {
	n := 0
	for idx, _ := range buf {
		buf[idx] = <-md.datastream
		n++
	}
	return n, nil
}

func (md mockDevice) Write(buf []byte) (int, error) {
	return 0, nil
}

func (md mockDevice) Close() error {
	close(md.datastream)
	return nil
}

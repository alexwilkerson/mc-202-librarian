package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

const (
	framesToRead = 8192 // Define the number of frames to read each time
	baseFreq     = 2371
	magicByte    = 0xE0
	// this is the length of 1 bit cycles in between the program name and the
	// rest of the data
	dataBufferLength = 122
	barByte          = 0xFF
)

var noteMap = buildNoteMap()

func buildNoteMap() map[int]Note {
	noteNames := []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	noteMap := make(map[int]Note)

	for i := 0; i < 64; i++ {
		noteMap[i] = Note{
			NoteNum:  i,
			NoteName: noteNames[i%12],
			Octave:   (i / 12) + 1,
		}
	}

	return noteMap
}

// generateSignChangeBits reads a WAV file and emits a stream of sign-change bits.
func generateSignChangeBits(decoder *wav.Decoder, offset bool) ([]int, error) {
	var bits []int

	var previous byte

	numChannels := decoder.NumChans
	bitDepth := decoder.BitDepth

	decoder.Rewind()

	buf := &audio.IntBuffer{Data: make([]int, framesToRead), Format: &audio.Format{}}

	// This is sometimes necessary as the first attempt at reading the file
	// will result in an error, but if we read rewind, then read read the first frame
	// before processing the rest of the file, it works. ??
	if offset {
		_, err := decoder.PCMBuffer(buf)
		if err != nil {
			return nil, fmt.Errorf("error reading offset buffer: %w", err)
		}
	}

	for {
		n, err := decoder.PCMBuffer(buf)
		if err != nil {
			return nil, err
		}

		if n == 0 || buf.Data == nil {
			break
		}

		for i := 0; i < len(buf.Data); i += int(numChannels) {
			var msb byte

			switch bitDepth {
			case 16:
				msb = byte(buf.Data[i] >> 8)
			case 24:
				msb = byte(buf.Data[i] >> 16)
			case 32:
				msb = byte(buf.Data[i] >> 24)
			default:
				return nil, fmt.Errorf("unsupported bit depth: %d", bitDepth)
			}

			signBit := msb & 0x80
			if signBit^previous != 0 {
				bits = append(bits, 1)
			} else {
				bits = append(bits, 0)
			}
			previous = signBit
		}
	}

	return bits, nil
}

const BaseFreq = 2371 // Set your BASE_FREQ
var BitMasks = []uint16{0x1, 0x2, 0x4, 0x8, 0x10, 0x20, 0x40, 0x80}

// generateBytes processes the sign change bits and assembles them into bytes.
func generateBytes(bitstream []int, framerate int) ([]byte, error) {
	framesPerBit := int(float64(framerate)*4/BaseFreq + 0.5)
	sample := make([]int, framesPerBit) // Slice to use as a circular buffer
	var sampleIndex int                 // Current index in the sample buffer

	// Fill the initial buffer with data
	for i := 0; i < framesPerBit-1; i++ {
		sample[i] = bitstream[i]
	}

	var result []byte
	signChanges := sum(sample) // Calculate initial sum of sign changes
	bitstreamIndex := framesPerBit - 1

	var (
		foundMagicByte bool
		magicByteIndex int
		previousByte   byte
		validByteIndex int = -1
		lastByteIndex  int
		insideBuffer   bool
	)

L1:
	for bitstreamIndex < len(bitstream) {
		if insideBuffer {
			for i := 0; i < dataBufferLength; i++ {
				if sum(bitstream[bitstreamIndex:bitstreamIndex+framesPerBit]) < 7 {
					return nil, fmt.Errorf("something went wrong: invalid data buffer")
				}
				bitstreamIndex += framesPerBit
			}

			insideBuffer = false

			// Refill the sample buffer
			for i := 0; i < framesPerBit && bitstreamIndex+i < len(bitstream); i++ {
				sample[sampleIndex] = bitstream[bitstreamIndex+i]
				sampleIndex = (sampleIndex + 1) % framesPerBit
			}

			signChanges = sum(sample)

			bitstreamIndex += framesPerBit
		}

		val := bitstream[bitstreamIndex]

		if val > 0 {
			signChanges++
		}
		if sample[sampleIndex] > 0 {
			signChanges--
		}

		// Update the circular buffer
		sample[sampleIndex] = val
		sampleIndex = (sampleIndex + 1) % framesPerBit

		if signChanges <= 4 {
			var (
				byteVal uint16
			)

			for _, mask := range BitMasks {
				if sum(bitstream[bitstreamIndex:bitstreamIndex+framesPerBit]) >= 7 {
					byteVal |= mask
				}
				bitstreamIndex += framesPerBit
			}

			// short circuit if we have not found the magic byte yet
			// therefore this must be invalid data
			if !foundMagicByte && byteVal != magicByte {
				continue
			}

			// the first three bytes proceeding the magic byte are the pattern number
			// byte 1 is the hundreds place, byte 2 is the tens place, and byte 3 is
			// the ones place. if any of these bytes are not between 0 and 9, we know
			// that the magic byte was found in error, so we should return to the frame
			// after the initial incorrect magic byte was found and continue iterating
			if foundMagicByte && (validByteIndex+1 == 1 || validByteIndex+1 == 2 || validByteIndex+1 == 3) {
				if int(byteVal) < 0 || int(byteVal) > 9 {
					// return to the frame after the initial incorrect byte and continue
					foundMagicByte = false
					bitstreamIndex = magicByteIndex + framesPerBit
					validByteIndex = -1
					magicByteIndex = 0
					result = result[:0]

					// Refill the sample buffer
					for i := 0; i < framesPerBit && bitstreamIndex+i < len(bitstream); i++ {
						sample[sampleIndex] = bitstream[bitstreamIndex+i]
						sampleIndex = (sampleIndex + 1) % framesPerBit
					}

					signChanges = sum(sample)

					bitstreamIndex += framesPerBit

					continue
				}
			}

			// check for stop bits.. if the stop bits are not 1s, we know this is
			// an invalid byte so we will skip it. The exception to this is the
			// last byte in the stream, which does not have stop bits. instead it
			// has a single base frequency cycle, then is followed by base freq Hz/2
			//
			// we check validByteIndex+1 != lastByteIndex because we haven't incremented
			// validByteIndex yet
			if lastByteIndex == 0 || validByteIndex+1 != lastByteIndex {
				for i := 0; i < 2; i++ {
					if sum(bitstream[bitstreamIndex:bitstreamIndex+framesPerBit]) < 7 {
						// return to the frame after the initial incorrect byte and continue
						bitstreamIndex = bitstreamIndex - framesPerBit*(8+i)

						// if we found the magic byte, we know that we are inside the data
						// buffer so there should be no invalid bytes. if we find an invalid
						// byte here, it likely means that we have not found the magic byte
						// yet, so we should skip this byte and return to the frame after
						// the initial incorrect magic byte was found and continue iterating
						// through the bitstream
						if foundMagicByte {
							foundMagicByte = false
							bitstreamIndex = magicByteIndex + framesPerBit
							validByteIndex = -1
							magicByteIndex = 0
							result = result[:0]
						}

						// Refill the sample buffer
						for i := 0; i < framesPerBit && bitstreamIndex+i < len(bitstream); i++ {
							sample[sampleIndex] = bitstream[bitstreamIndex+i]
							sampleIndex = (sampleIndex + 1) % framesPerBit
						}

						signChanges = sum(sample)

						bitstreamIndex += framesPerBit

						continue L1
					}
					bitstreamIndex += framesPerBit
				}
			}

			// VALID BYTE
			validByteIndex++

			if byteVal == magicByte {
				foundMagicByte = true
				magicByteIndex = bitstreamIndex - framesPerBit*11
			}

			if validByteIndex == 5 {
				lastByteIndex = validByteIndex + int((uint16(previousByte)<<8)+uint16(byteVal)) + 4
			}

			result = append(result, byte(byteVal))

			previousByte = byte(byteVal)

			// check for last byte
			if lastByteIndex != 0 && validByteIndex == lastByteIndex {
				break
			}

			if validByteIndex == 3 {
				insideBuffer = true
				continue
			}

			// Refill the sample buffer
			for i := 0; i < framesPerBit && bitstreamIndex+i < len(bitstream); i++ {
				sample[sampleIndex] = bitstream[bitstreamIndex+i]
				sampleIndex = (sampleIndex + 1) % framesPerBit
			}

			signChanges = sum(sample)

			bitstreamIndex += framesPerBit
		} else {
			bitstreamIndex++
		}
	}

	if len(result) != lastByteIndex+1 {
		return nil, fmt.Errorf("something went wrong: invalid number of bytes: %d", len(result))
	}

	return result, nil
}

// sum returns the sum of the elements in the slice.
func sum(slice []int) int {
	total := 0
	for _, v := range slice {
		total += v
	}
	return total
}

type Sequence struct {
	MagicByte     byte
	ProgramNumber int
	TotalLines    int
	Notes         []NoteLine
	ParityByte1   byte
	TotalLines2   int
	ParityByte2   byte
}

type NoteLine struct {
	NoteNum    int
	NoteName   string
	Octave     int
	StepLength int
	GateLength int
	Portamento bool
	Accent     bool
	Bar        bool
}

type Note struct {
	NoteNum  int
	NoteName string
	Octave   int
}

func validateBytes(data []byte) error {
	if len(data) < 10 {
		return fmt.Errorf("validation failed - invalid number of bytes: %d", len(data))
	}

	if data[0] != magicByte {
		return fmt.Errorf("validation failed - invalid magic byte: %02X", data[0])
	}

	if int(data[1]) < 0 || int(data[1]) > 9 {
		return fmt.Errorf("validation failed - invalid program number byte 1: %d", int(data[1]))
	}

	if int(data[2]) < 0 || int(data[2]) > 9 {
		return fmt.Errorf("validation failed - invalid program number byte 2: %d", int(data[2]))
	}

	if int(data[3]) < 0 || int(data[3]) > 9 {
		return fmt.Errorf("validation failed - invalid program number byte 3: %d", int(data[3]))
	}

	totalLines := int(binary.BigEndian.Uint16(data[4:6]))

	// TODO: verify maximum line count
	if totalLines < 0 || totalLines > 999 {
		return fmt.Errorf("validation failed - invalid total lines: %d", totalLines)
	}

	if len(data) < totalLines+6 {
		return fmt.Errorf("validation failed - invalid number of bytes (did not match total lines 1): %d", len(data))
	}

	bytesum := totalLines
	var noteLines int

	for i := 0; i < totalLines; i++ {
		bytesum += int(data[6+i])

		if data[6+i] != barByte {
			noteLines++
		}
	}

	checksum := int8(bytesum)

	if noteLines%3 != 0 {
		return fmt.Errorf("validation failed - invalid number of note lines: %d", noteLines)
	}

	parityByte := data[6+totalLines]

	computedParityByte := int8(parityByte) + int8(data[6+totalLines+1])

	if computedParityByte+checksum != 0 {
		return fmt.Errorf("validation failed - invalid parity byte 1: computed: (%d, %02X) checksum: (%d, %02X)", computedParityByte, byte(computedParityByte), checksum, byte(checksum))
	}

	endLineCount := int(binary.BigEndian.Uint16(data[6+totalLines+1 : 6+totalLines+3]))

	if totalLines != endLineCount {
		return fmt.Errorf("validation failed - line count does not match: %d != %d", totalLines, endLineCount)
	}

	computedLineCount := int8(data[6+totalLines+1]) + int8(data[6+totalLines+2])

	lineCountParityByte := int8(data[6+totalLines+3])

	if computedLineCount+lineCountParityByte != 0 {
		return fmt.Errorf("validation failed - invalid parity byte 2: computed: (%d, %02X) lineCountParityByte: (%d, %02X)", computedLineCount, byte(computedLineCount), lineCountParityByte, byte(lineCountParityByte))
	}

	return nil
}

func parseBytes(data []byte) (*Sequence, error) {
	if err := validateBytes(data); err != nil {
		return nil, err
	}

	sequence := Sequence{
		MagicByte:     data[0],
		ProgramNumber: int(data[1])*100 + int(data[2])*10 + int(data[3]),
		TotalLines:    int(binary.BigEndian.Uint16(data[4:6])),
	}

	i := 6
	for i < len(data)-4 { // Reserve the last 4 bytes for parity and line count
		if data[i] == barByte {
			sequence.Notes = append(sequence.Notes, NoteLine{Bar: true})
			continue
		}

		noteNum := int(data[i+2] & 0b00111111)

		sequence.Notes = append(sequence.Notes, NoteLine{
			NoteNum:    noteNum,
			NoteName:   noteMap[noteNum].NoteName,
			Octave:     noteMap[noteNum].Octave,
			StepLength: int(data[i]),
			GateLength: int(data[i+1]),
			Portamento: data[i+2]&0b10000000 != 0,
			Accent:     data[i+2]&0b01000000 != 0,
		})
		i += 3
	}

	sequence.ParityByte1 = data[i]
	sequence.TotalLines2 = int(binary.BigEndian.Uint16(data[i+1 : i+3]))
	sequence.ParityByte2 = data[i+3]

	return &sequence, nil
}

func (s *Sequence) String() string {
	var sb strings.Builder

	// pretty print the program
	sb.WriteString(fmt.Sprintf("Program Number: %d\n", s.ProgramNumber))
	sb.WriteString(fmt.Sprintf("Total Lines: %d\n", s.TotalLines))
	sb.WriteString("Notes:")
	for _, note := range s.Notes {
		sb.WriteString("\n")
		if note.Bar {
			sb.WriteString("\tBar\n")
			continue
		}

		sb.WriteString(fmt.Sprintf("\tNote Number: %d\n", note.NoteNum))
		sb.WriteString(fmt.Sprintf("\tNote Name: %s\n", note.NoteName))
		sb.WriteString(fmt.Sprintf("\tOctave: %d\n", note.Octave))
		sb.WriteString(fmt.Sprintf("\tStep Length: %d\n", note.StepLength))
		sb.WriteString(fmt.Sprintf("\tGate Length: %d\n", note.GateLength))
		sb.WriteString(fmt.Sprintf("\tPortamento: %t\n", note.Portamento))
		sb.WriteString(fmt.Sprintf("\tAccent: %t\n", note.Accent))
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("Parity Byte 1: %02X\n", s.ParityByte1))
	sb.WriteString(fmt.Sprintf("Total Lines 2: %d\n", s.TotalLines2))
	sb.WriteString(fmt.Sprintf("Parity Byte 2: %02X\n", s.ParityByte2))

	return sb.String()
}

func main() {
	encodePtr := flag.Bool("encode", false, "encode a file")

	decodePtr := flag.Bool("decode", false, "decode a file")

	jsonPtr := flag.Bool("json", false, "output json")

	fileNamePtr := flag.String("file", "", "file to encode/decode")

	flag.Parse()

	if *encodePtr && *decodePtr {
		fmt.Println("cannot encode and decode at the same time")
		os.Exit(1)
	}

	if !*encodePtr && !*decodePtr {
		fmt.Println("must specify encode or decode")
		os.Exit(1)
	}

	if *encodePtr {
		// encode
		fmt.Println("encode")
		os.Exit(0)
	}

	if *decodePtr {
		waveFile, err := os.Open(*fileNamePtr)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer waveFile.Close()

		decoder := wav.NewDecoder(waveFile)
		if !decoder.IsValidFile() {
			fmt.Println("invalid wav file")
			os.Exit(1)
		}

		sampleRate := decoder.SampleRate

		signBits, err := generateSignChangeBits(decoder, false)
		if err != nil {
			fmt.Println("problem generating sign change bits:", err)
			os.Exit(1)
		}

		bytes, err := generateBytes(signBits, int(sampleRate))
		if err != nil {
			fmt.Println(err)
			fmt.Println("trying again with offset...")

			signBits, err = generateSignChangeBits(decoder, true)
			if err != nil {
				fmt.Println("problem generating sign change bits:", err)
				os.Exit(1)
			}

			bytes, err = generateBytes(signBits, int(sampleRate))
			if err != nil {
				fmt.Print("second attempt at generating bytes failed:", err)
				os.Exit(1)
			}
		}

		fmt.Println("Success!")

		fmt.Println()

		for _, b := range bytes {
			fmt.Printf("%02X ", b)
		}

		fmt.Println()
		fmt.Println()

		sequence, err := parseBytes(bytes)
		if err != nil {
			fmt.Println("problem parsing bytes:", err)
			os.Exit(1)
		}

		_ = sequence

		fmt.Println(sequence)

		if *jsonPtr {
			name := strings.TrimSuffix(*fileNamePtr, ".wav")

			f, err := os.Create(name + ".json")
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			defer f.Close()

			prettyJSON, err := json.MarshalIndent(sequence, "", "    ")
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

			_, err = f.Write(prettyJSON)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

			fmt.Println("json file written to", name+".json")
		}
	}
}

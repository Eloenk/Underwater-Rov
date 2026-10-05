package video

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	vidiocQuerycap       = 0x80685600
	vidiocEnumFmt        = 0xc0405602
	vidiocSFmt           = 0xc0d05605
	vidiocReqbufs        = 0xc0145608
	vidiocQuerybuf       = 0xc0585609
	vidiocQbuf           = 0xc058560f
	vidiocDqbuf          = 0xc0585611
	vidiocStreamon       = 0x40045612
	vidiocStreamoff      = 0x40045613
	vidiocEnumFramesizes = 0xc02c564a
	vidiocEnumFrameival  = 0xc034564b
	vidiocSCtrl          = 0xc008561c
	v4l2CidBrightness    = 0x00980900
	v4l2CidContrast      = 0x00980901
	v4l2CidHFlip         = 0x00980914
	v4l2CidVFlip         = 0x00980915
)

type v4l2Control struct {
	ID    uint32
	Value int32
}

type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

type v4l2Fmtdesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	Reserved    [4]uint32
}

type v4l2FrmsizeDiscrete struct {
	Width  uint32
	Height uint32
}

type v4l2Frmsizeenum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	Data        [24]byte
	Reserved    [2]uint32
}

type v4l2Fract struct {
	Numerator   uint32
	Denominator uint32
}

type v4l2Frmivalenum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	Data        [24]byte
	Reserved    [2]uint32
}

type v4l2PixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	Encoding     uint32
	Quantization uint32
	XferFunc     uint32
}

type v4l2Format struct {
	Type uint32
	_    [4]byte
	Fmt  [200]byte
}

type v4l2Requestbuffers struct {
	Count    uint32
	Type     uint32
	Memory   uint32
	Reserved [2]uint32
}

type v4l2Timeval struct {
	Sec  int64
	Usec int64
}

type v4l2Buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	_         [4]byte
	Timestamp v4l2Timeval
	Timecode  [16]byte
	Sequence  uint32
	Memory    uint32
	Offset    uint64
	Length    uint32
	Reserved2 uint32
	Reserved  uint32
	_         [4]byte
}

type V4L2Camera struct {
	fd          int
	buffers     [][]byte
	width       int
	height      int
	pixelFormat uint32
	isStreaming bool
}

func OpenV4L2Camera(devPath string, width, height, fps int) (*V4L2Camera, error) {
	fd, err := syscall.Open(devPath, syscall.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	var fmtStruct v4l2Format
	fmtStruct.Type = 1
	pix := (*v4l2PixFormat)(unsafe.Pointer(&fmtStruct.Fmt[0]))
	pix.Width = uint32(width)
	pix.Height = uint32(height)
	pixFormat := uint32(0x47504a4d)
	pix.PixelFormat = pixFormat
	pix.Field = 1

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocSFmt), uintptr(unsafe.Pointer(&fmtStruct)))
	if errno != 0 {
		pixFormat = 0x56595559
		pix.PixelFormat = pixFormat
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocSFmt), uintptr(unsafe.Pointer(&fmtStruct)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, errno
		}
	}

	var req v4l2Requestbuffers
	req.Count = 4
	req.Type = 1
	req.Memory = 1

	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocReqbufs), uintptr(unsafe.Pointer(&req)))
	if errno != 0 || req.Count == 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("vidiocReqbufs failed: %v", errno)
	}

	mappedBuffers := make([][]byte, req.Count)
	for i := uint32(0); i < req.Count; i++ {
		var buf v4l2Buffer
		buf.Type = 1
		buf.Memory = 1
		buf.Index = i

		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocQuerybuf), uintptr(unsafe.Pointer(&buf)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("vidiocQuerybuf failed: %v", errno)
		}

		mdata, err := syscall.Mmap(fd, int64(buf.Offset), int(buf.Length), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if err != nil {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("mmap failed: %w", err)
		}
		mappedBuffers[i] = mdata

		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocQbuf), uintptr(unsafe.Pointer(&buf)))
		if errno != 0 {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("vidiocQbuf failed: %v", errno)
		}
	}

	typeVal := uint32(1)
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocStreamon), uintptr(unsafe.Pointer(&typeVal)))
	if errno != 0 {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("vidiocStreamon failed: %v", errno)
	}

	return &V4L2Camera{
		fd:          fd,
		buffers:     mappedBuffers,
		width:       width,
		height:      height,
		pixelFormat: pixFormat,
		isStreaming: true,
	}, nil
}

func (cam *V4L2Camera) ReadFrame() ([]byte, error) {
	if !cam.isStreaming || cam.fd <= 0 {
		return nil, fmt.Errorf("camera not active")
	}

	var buf v4l2Buffer
	buf.Type = 1
	buf.Memory = 1

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(cam.fd), uintptr(vidiocDqbuf), uintptr(unsafe.Pointer(&buf)))
	if errno != 0 {
		return nil, errno
	}

	idx := buf.Index
	length := buf.BytesUsed
	if idx >= uint32(len(cam.buffers)) || length == 0 {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(cam.fd), uintptr(vidiocQbuf), uintptr(unsafe.Pointer(&buf)))
		return nil, fmt.Errorf("invalid frame dequeued")
	}

	frame := make([]byte, length)
	copy(frame, cam.buffers[idx][:length])

	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(cam.fd), uintptr(vidiocQbuf), uintptr(unsafe.Pointer(&buf)))

	if cam.pixelFormat == 0x56595559 {
		jpg, err := yuyvToJPEG(frame, cam.width, cam.height, 80)
		if err == nil && len(jpg) > 0 {
			return jpg, nil
		}
	}

	return frame, nil
}

func yuyvToJPEG(yuyv []byte, w, h, quality int) ([]byte, error) {
	if len(yuyv) < w*h*2 {
		return nil, fmt.Errorf("buffer too small for YUYV frame")
	}

	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio422)

	yIdx := 0
	cIdx := 0
	srcIdx := 0

	for y := 0; y < h; y++ {
		for x := 0; x < w; x += 2 {
			y0 := yuyv[srcIdx]
			u0 := yuyv[srcIdx+1]
			y1 := yuyv[srcIdx+2]
			v0 := yuyv[srcIdx+3]
			srcIdx += 4

			img.Y[yIdx] = y0
			img.Y[yIdx+1] = y1
			yIdx += 2

			img.Cb[cIdx] = u0
			img.Cr[cIdx] = v0
			cIdx++
		}
	}

	var buf bytes.Buffer
	if quality <= 0 {
		quality = 80
	}
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (cam *V4L2Camera) Close() {
	if cam.fd <= 0 {
		return
	}
	cam.isStreaming = false
	typeVal := uint32(1)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(cam.fd), uintptr(vidiocStreamoff), uintptr(unsafe.Pointer(&typeVal)))

	for _, b := range cam.buffers {
		_ = syscall.Munmap(b)
	}
	_ = syscall.Close(cam.fd)
	cam.fd = -1
}

func (cam *V4L2Camera) SetControl(id uint32, value int32) error {
	if cam.fd <= 0 {
		return fmt.Errorf("camera closed")
	}
	ctrl := v4l2Control{
		ID:    id,
		Value: value,
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(cam.fd), uintptr(vidiocSCtrl), uintptr(unsafe.Pointer(&ctrl)))
	if errno != 0 {
		return errno
	}
	return nil
}

func resolveUSBPath(devPath string) (string, error) {
	base := filepath.Base(devPath)
	link := filepath.Join("/sys/class/video4linux", base, "device")
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func queryV4L2Device(devPath string) (string, []Format, error) {
	fd, err := syscall.Open(devPath, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", nil, err
	}
	defer syscall.Close(fd)

	var cap v4l2Capability
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocQuerycap), uintptr(unsafe.Pointer(&cap))); errno != 0 {
		return "", nil, errno
	}
	name := cStr(cap.Card[:])

	var formats []Format
	for idx := uint32(0); ; idx++ {
		fmtDesc := v4l2Fmtdesc{Index: idx, Type: 1}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocEnumFmt), uintptr(unsafe.Pointer(&fmtDesc)))
		if errno != 0 {
			break
		}
		fourCC := fourCCString(fmtDesc.PixelFormat)

		var resFound bool
		for sIdx := uint32(0); ; sIdx++ {
			fSize := v4l2Frmsizeenum{
				Index:       sIdx,
				PixelFormat: fmtDesc.PixelFormat,
			}
			_, _, errno2 := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocEnumFramesizes), uintptr(unsafe.Pointer(&fSize)))
			if errno2 != 0 {
				break
			}

			if fSize.Type == 1 {
				resFound = true
				disc := (*v4l2FrmsizeDiscrete)(unsafe.Pointer(&fSize.Data[0]))
				w := int(disc.Width)
				h := int(disc.Height)

				maxFps := 30
				for iIdx := uint32(0); ; iIdx++ {
					fIval := v4l2Frmivalenum{
						Index:       iIdx,
						PixelFormat: fmtDesc.PixelFormat,
						Width:       disc.Width,
						Height:      disc.Height,
					}
					_, _, errno3 := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(vidiocEnumFrameival), uintptr(unsafe.Pointer(&fIval)))
					if errno3 != 0 {
						break
					}
					if fIval.Type == 1 {
						fract := (*v4l2Fract)(unsafe.Pointer(&fIval.Data[0]))
						if fract.Numerator > 0 && fract.Denominator > 0 {
							calcFps := int(fract.Denominator / fract.Numerator)
							if calcFps > maxFps || iIdx == 0 {
								maxFps = calcFps
							}
						}
					}
				}

				formats = append(formats, Format{
					FourCC: fourCC,
					Width:  w,
					Height: h,
					MaxFPS: maxFps,
				})
			}
		}

		if !resFound {
			continue
		}
	}

	if len(formats) == 0 {
		return "", nil, fmt.Errorf("no supported hardware video formats enumerated for device %s", devPath)
	}

	return name, formats, nil
}

func fourCCString(v uint32) string {
	b := []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	return string(b)
}

func cStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}



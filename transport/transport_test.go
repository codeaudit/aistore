/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */
package transport_test

// How to run (examples):
//
// 1) run all tests while redirecting errors to standard error:
// go test -v -logtostderr=true
//
// 2) run a given test (name matching "Multi") with debug enabled:
// DFC_STREAM_DEBUG=1 go test -v -run=Multi

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/dfcpub/common"
	"github.com/NVIDIA/dfcpub/iosgl"
	"github.com/NVIDIA/dfcpub/transport"
)

const (
	text1 = `Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut
labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat.`
	text2 = `Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur.
Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum.`
	text3 = `Et harum quidem rerum facilis est et expedita distinctio. Nam libero tempore, cum soluta nobis est
eligendi optio, cumque nihil impedit, quo minus id, quod maxime placeat, facere possimus, omnis voluptas assumenda est, omnis dolor repellendus.`
	text4 = `Temporibus autem quibusdam et aut officiis debitis aut rerum necessitatibus saepe eveniet,
ut et voluptates repudiandae sint et molestiae non-recusandae.`
)

func Example_Headers() {
	f := func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}
		var (
			hdr       transport.Header
			hlen, off int
		)
		for {
			hlen = int(binary.BigEndian.Uint64(body[off:]))
			off += 8
			hdr = transport.ExtHeader(body[off:], hlen)
			if !hdr.IsLast() {
				fmt.Fprintf(os.Stdout, "%+v (%d)\n", hdr, hlen)
				off += hlen + int(hdr.Dsize)
			} else {
				break
			}
		}
	}

	ts := httptest.NewServer(http.HandlerFunc(f))
	defer ts.Close()

	client := &http.Client{Transport: &http.Transport{}}

	stream := transport.NewStream(client, ts.URL)

	sendText(stream, text1, text2)
	stream.Fin()
	// Output:
	// {Bucket:abc Objname:X Opaque:[] Dsize:231} (36)
	// {Bucket:abracadabra Objname:p/q/s Opaque:[49 50 51] Dsize:213} (51)
}

func sendText(stream *transport.Stream, txt1, txt2 string) {
	sgl1 := iosgl.NewSGL(0)
	sgl1.Write([]byte(txt1))
	stream.SendAsync(transport.Header{"abc", "X", nil, sgl1.Size()}, sgl1)

	sgl2 := iosgl.NewSGL(0)
	sgl2.Write([]byte(txt2))
	stream.SendAsync(transport.Header{"abracadabra", "p/q/s", []byte{'1', '2', '3'}, sgl2.Size()}, sgl2)
}

func Example_Mux() {
	receive := func(w http.ResponseWriter, hdr transport.Header, objReader io.Reader) {
		object, err := ioutil.ReadAll(objReader)
		if err != nil && err != io.EOF {
			panic(err)
		}
		if int64(len(object)) != hdr.Dsize {
			panic(fmt.Sprintf("size %d != %d", len(object), hdr.Dsize))
		}
		fmt.Fprintf(os.Stdout, "%s...\n", string(object[:16]))
	}
	mux := http.NewServeMux()

	transport.SetMux(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	path := transport.Register("dummy-rx", receive)
	client := &http.Client{Transport: &http.Transport{}}
	url := ts.URL + path
	stream := transport.NewStream(client, url)

	sendText(stream, text1, text2)

	time.Sleep(time.Second * 2)

	sendText(stream, text3, text4)
	stream.Fin()

	// Output:
	// Lorem ipsum dolo...
	// Duis aute irure ...
	// Et harum quidem ...
	// Temporibus autem...
}

// test random streaming
func Test_OneStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping not short")
	}
	mux := http.NewServeMux()

	transport.SetMux(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	streamWrite10GB(99, nil, ts)
}

func Test_MultiStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping not short")
	}
	mux := http.NewServeMux()

	transport.SetMux(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	wg := &sync.WaitGroup{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go streamWrite10GB(i, wg, ts)
	}
	wg.Wait()
}

//
// test helpers
//

func streamWrite10GB(ii int, wg *sync.WaitGroup, ts *httptest.Server) {
	if wg != nil {
		defer wg.Done()
	}
	path := transport.Register(fmt.Sprintf("rand-rx-%d", ii), testReceive)
	client := &http.Client{Transport: &http.Transport{}}
	url := ts.URL + path
	stream := transport.NewStream(client, url)

	random := newRand(time.Now().UnixNano())
	size, num, prevsize := int64(0), 0, int64(0)
	slab := iosgl.SelectSlab(32 * common.KiB)
	for size < common.GiB*10 {
		hdr := genRandomHeader(random)
		reader := newRandReader(random, hdr, slab)
		stream.SendAsync(hdr, reader)
		size += hdr.Dsize
		if size-prevsize >= common.GiB {
			fmt.Fprintf(os.Stdout, "[%2d]: %d GiB\n", ii, size/common.GiB)
			prevsize = size
		}
		num++
	}
	stream.Fin()
	fmt.Fprintf(os.Stdout, "[%2d]: objects: %d, total size: %d(%d MiB)\n", ii, num, size, size/common.MiB)
}

func testReceive(w http.ResponseWriter, hdr transport.Header, objReader io.Reader) {
	slab := iosgl.SelectSlab(32 * common.KiB)
	buf := slab.Alloc()
	written, err := io.CopyBuffer(ioutil.Discard, objReader, buf)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if written != hdr.Dsize {
		panic(fmt.Sprintf("size %d != %d", written, hdr.Dsize))
	}
	slab.Free(buf)
}

func newRand(seed int64) *rand.Rand {
	src := rand.NewSource(seed)
	random := rand.New(src)
	return random
}

func genRandomHeader(random *rand.Rand) (hdr transport.Header) {
	hdr.Bucket = strconv.FormatInt(random.Int63(), 16)
	hdr.Objname = hdr.Bucket + "/" + strconv.FormatInt(random.Int63(), 10)
	x := random.Int63()
	y := x & 3
	switch y {
	case 0:
		hdr.Dsize = (x & 0xffffff) + 1
	case 1:
		hdr.Dsize = (x & 0xfffff) + 1
	case 2:
		hdr.Dsize = (x & 0xffff) + 1
	default:
		hdr.Dsize = (x & 0xfff) + 1
	}
	return
}

type randReader struct {
	buf  []byte
	hdr  transport.Header
	slab *iosgl.Slab
	off  int64
}

func newRandReader(random *rand.Rand, hdr transport.Header, slab *iosgl.Slab) *randReader {
	buf := slab.Alloc()
	_, err := random.Read(buf)
	if err != nil {
		panic("Failed read rand: " + err.Error())
	}
	return &randReader{buf: buf, hdr: hdr, slab: slab}
}

func (r *randReader) Read(p []byte) (n int, err error) {
	for {
		rem := r.hdr.Dsize - r.off
		if rem == 0 {
			return n, io.EOF
		}
		l64 := common.MinI64(rem, int64(len(p)-n))
		if l64 == 0 {
			return
		}
		nr := copy(p[n:n+int(l64)], r.buf)
		n += nr
		r.off += int64(nr)
	}
}

func (r *randReader) Close() error {
	r.slab.Free(r.buf)
	return nil
}
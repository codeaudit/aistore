/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */
package memsys_test

// E.g. running this specific test:
//
// go test -v -run=SGLS -verbose=true -logtostderr=true
//
// For more examples, see other tests in this directory
//

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"runtime/debug"
	"sync"
	"testing"

	"github.com/NVIDIA/dfcpub/memsys"
)

const (
	objsize = 100
	objects = 1000
	workers = 1000
)

// creates 2 SGL, put some data to one of them and them copy from SGL to SGL
func TestSGLStressN(t *testing.T) {
	mem := &memsys.Mem2{MinPctFree: 50, Name: "cmem", Debug: verbose}
	err := mem.Init()
	if err != nil {
		t.Fatal(err)
	}
	wg := &sync.WaitGroup{}
	fn := func(id int) {
		defer wg.Done()
		for i := 0; i < objects; i++ {
			sglR := mem.NewSGL(128)
			sglW := mem.NewSGL(128)
			bufR := make([]byte, objsize)

			// fill buffer with "unique content"
			for j := 0; j < objsize; j++ {
				bufR[j] = byte('A') + byte(i%26)
			}

			// save buffer to SGL
			br := bytes.NewReader(bufR)
			_, err := io.Copy(sglR, br)
			checkFatal(err, t)
			// copy SGL to SGL
			rr := memsys.NewReader(sglR)
			_, err = io.Copy(sglW, rr)
			checkFatal(err, t)

			// read SGL from destination and compare with the original
			var bufW []byte
			bufW, err = ioutil.ReadAll(memsys.NewReader(sglW))
			checkFatal(err, t)
			for j := 0; j < objsize; j++ {
				if bufW[j] != bufR[j] {
					fmt.Printf("IN : %s\nOUT: %s\n", string(bufR), string(bufW))
					t.Fatalf("Step %d failed", i)
				}
			}
			sglR.Free() // removing these two lines fixes the test
			sglW.Free()
		}
		if id > 0 && id%100 == 0 {
			fmt.Printf("%d done\n", id)
		}
	}
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go fn(n)
	}
	wg.Wait()
}

func checkFatal(err error, t *testing.T) {
	if err != nil {
		fmt.Printf("FATAL: %v\n", err)
		debug.PrintStack()
		t.Fatalf("FATAL: %v", err)
	}
}

/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */

// Package dfc is a scalable object-storage based caching system with Amazon and Google Cloud backends.
package dfc_test

import (
	"io"
	"math/rand"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/dfcpub/api"
	"github.com/NVIDIA/dfcpub/dfc"
	"github.com/NVIDIA/dfcpub/iosgl"
	"github.com/NVIDIA/dfcpub/pkg/client"
	"github.com/NVIDIA/dfcpub/pkg/client/readers"
)

const (
	dummySrcURL = "http://127.0.0.1:10088"
	badChecksum = "badChecksumValue"
)

func TestReplicationReceiveOneObject(t *testing.T) {
	const (
		object   = "TestReplicationReceiveOneObject"
		fileSize = int64(1024)
	)
	reader, err := readers.NewRandReader(fileSize, false)
	checkFatal(err, t)

	proxyURLRepl := getPrimaryReplicationURL(t, proxyURLRO)
	proxyURL := getPrimaryURL(t, proxyURLRO)
	xxhash := getXXHashChecksum(t, reader)

	createFreshLocalBucket(t, proxyURL, TestLocalBucketName)
	defer deleteLocalBucket(proxyURL, TestLocalBucketName, t)

	tlogf("Sending %s/%s for replication. Destination proxy: %s\n", TestLocalBucketName, object, proxyURLRepl)
	statusCode := httpReplicationPut(t, dummySrcURL, proxyURLRepl, TestLocalBucketName, object, xxhash, reader)

	if statusCode >= http.StatusBadRequest {
		t.Errorf("Expected status code %d, received status code %d", http.StatusOK, statusCode)
	}

	tlogf("Sending %s/%s for replication. Destination proxy: %s\n", clibucket, object, proxyURLRepl)
	statusCode = httpReplicationPut(t, dummySrcURL, proxyURLRepl, clibucket, object, xxhash, reader)

	if statusCode >= http.StatusBadRequest {
		t.Errorf("Expected status code %d, received status code %d", http.StatusOK, statusCode)
	}

	client.Del(proxyURL, clibucket, object, nil, nil, true)
}

func TestReplicationReceiveOneObjectNoChecksum(t *testing.T) {
	const (
		object   = "TestReplicationReceiveOneObjectNoChecksum"
		fileSize = int64(1024)
	)
	reader, err := readers.NewRandReader(fileSize, false)
	checkFatal(err, t)

	proxyURLRepl := getPrimaryReplicationURL(t, proxyURLRO)
	proxyURL := getPrimaryURL(t, proxyURLRO)

	createFreshLocalBucket(t, proxyURL, TestLocalBucketName)
	defer deleteLocalBucket(proxyURL, TestLocalBucketName, t)

	url := proxyURLRepl + api.URLPath(api.Version, api.Objects, TestLocalBucketName, object)
	req, err := http.NewRequest(http.MethodPut, url, reader)
	checkFatal(err, t)
	req.GetBody = func() (io.ReadCloser, error) {
		return reader.Open()
	}

	req.Header.Add(api.HeaderDFCReplicationSrc, dummySrcURL)

	tlogf("Sending %s/%s for replication. Destination proxy: %s. Expecting to fail\n", TestLocalBucketName, object, proxyURLRepl)
	resp, err := http.DefaultClient.Do(req)
	checkFatal(err, t)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("Replication PUT to local bucket without checksum didn't fail")
	}
	resp.Body.Close()

	url = proxyURLRepl + api.URLPath(api.Version, api.Objects, clibucket, object)
	req, err = http.NewRequest(http.MethodPut, url, reader)
	checkFatal(err, t)
	req.GetBody = func() (io.ReadCloser, error) {
		return reader.Open()
	}

	req.Header.Add(api.HeaderDFCReplicationSrc, dummySrcURL)

	tlogf("Sending %s/%s for replication. Destination proxy: %s. Expecting to fail\n", clibucket, object, proxyURLRepl)
	resp, err = http.DefaultClient.Do(req)
	checkFatal(err, t)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("Replication PUT to cloud bucket without checksum didn't fail")
	}
	resp.Body.Close()
}

func TestReplicationReceiveOneObjectBadChecksum(t *testing.T) {
	const (
		object   = "TestReplicationReceiveOneObjectBadChecksum"
		fileSize = int64(1024)
	)
	reader, err := readers.NewRandReader(fileSize, false)
	checkFatal(err, t)

	proxyURLRepl := getPrimaryReplicationURL(t, proxyURLRO)
	proxyURL := getPrimaryURL(t, proxyURLRO)

	createFreshLocalBucket(t, proxyURL, TestLocalBucketName)
	defer deleteLocalBucket(proxyURL, TestLocalBucketName, t)

	tlogf("Sending %s/%s for replication. Destination proxy: %s. Expecting to fail\n", TestLocalBucketName, object, proxyURLRepl)
	statusCode := httpReplicationPut(t, dummySrcURL, proxyURLRepl, TestLocalBucketName, object, badChecksum, reader)

	if statusCode == http.StatusOK {
		t.Errorf("Replication PUT to local bucket with bad checksum didn't fail")
	}

	tlogf("Sending %s/%s for replication. Destination proxy: %s. Expecting to fail\n", clibucket, object, proxyURLRepl)
	statusCode = httpReplicationPut(t, dummySrcURL, proxyURLRepl, clibucket, object, badChecksum, reader)

	if statusCode == http.StatusOK {
		t.Errorf("Replication PUT to cloud bucket with bad checksum didn't fail")
	}
}

func TestReplicationReceiveManyObjectsCloudBucket(t *testing.T) {
	const (
		fileSize  = uint64(1024)
		numFiles  = 100
		seedValue = int64(111)
	)
	var (
		proxyURLRepl = getPrimaryReplicationURL(t, proxyURLRO)
		bucket       = clibucket
		size         = fileSize
		r            client.Reader
		sgl          *iosgl.SGL
		errCnt       int
		err          error
	)

	if testing.Short() {
		t.Skip("Skipping test in short mode.")
	}

	tlogf("Sending %d files (cloud bucket: %s) for replication...\n", numFiles, bucket)

	fileList := make([]string, 0, numFiles)
	src := rand.NewSource(seedValue)
	random := rand.New(src)
	for i := 0; i < numFiles; i++ {
		fname := client.FastRandomFilename(random, fnlen)
		fileList = append(fileList, fname)
	}

	if size == 0 {
		size = uint64(random.Intn(1024)+1) * 1024
	}

	if usingSG {
		sgl = iosgl.NewSGL(size)
		defer sgl.Free()
	}

	for idx, fname := range fileList {
		object := SmokeStr + "/" + fname
		if sgl != nil {
			sgl.Reset()
			r, err = readers.NewSGReader(sgl, int64(size), true)
		} else {
			r, err = readers.NewReader(readers.ParamReader{Type: readerType, SGL: nil, Path: SmokeDir, Name: fname, Size: int64(size)})
		}

		if err != nil {
			t.Error(err)
			tlogf("Failed to generate random file %s, err: %v\n", filepath.Join(SmokeDir, fname), err)
		}

		tlogf("Receiving replica: %s (%d/%d)...\n", object, idx+1, numFiles)
		statusCode := httpReplicationPut(t, dummySrcURL, proxyURLRepl, bucket, object, r.XXHash(), r)
		if statusCode >= http.StatusBadRequest {
			errCnt++
			t.Errorf("ERROR: Expected status code %d, received status code %d\n", http.StatusOK, statusCode)
		}
	}
	tlogf("Successful: %d/%d. Failed: %d/%d\n", numFiles-errCnt, numFiles, errCnt, numFiles)
}

func getPrimaryReplicationURL(t *testing.T, proxyURL string) string {
	smap, err := client.GetClusterMap(proxyURL)
	if err != nil {
		t.Fatalf("Failed to get primary proxy replication URL, error: %v", err)
	}
	return smap.ProxySI.ReplNet.DirectURL
}

func getXXHashChecksum(t *testing.T, reader io.Reader) string {
	buf, slab := iosgl.AllocFromSlab(0)
	xxhashval, errstr := dfc.ComputeXXHash(reader, buf)
	slab.Free(buf)
	if errstr != "" {
		t.Fatal("Failed to compute xxhash checksum")
	}
	return xxhashval
}

func httpReplicationPut(t *testing.T, srcURL, dstProxyURL, bucket, object, xxhash string, reader client.Reader) (statusCode int) {
	url := dstProxyURL + api.URLPath(api.Version, api.Objects, bucket, object)
	req, err := http.NewRequest(http.MethodPut, url, reader)
	checkFatal(err, t)
	req.GetBody = func() (io.ReadCloser, error) {
		return reader.Open()
	}

	req.Header.Add(api.HeaderDFCReplicationSrc, srcURL)
	req.Header.Add(api.HeaderDFCChecksumType, dfc.ChecksumXXHash)
	req.Header.Add(api.HeaderDFCChecksumVal, xxhash)

	resp, err := http.DefaultClient.Do(req)
	checkFatal(err, t)
	statusCode = resp.StatusCode
	resp.Body.Close()
	return
}
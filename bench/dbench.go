package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	hummingbird "hummingbird/common"
)

type DirectObject struct {
	Url  string
	Data []byte
}

func (obj *DirectObject) Put() bool {
	req, _ := http.NewRequest("PUT", obj.Url, bytes.NewReader(obj.Data))
	req.Header.Set("Content-Length", strconv.FormatInt(int64(len(obj.Data)), 10))
	req.Header.Set("X-Timestamp", hummingbird.GetTimestamp())
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(obj.Data))
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err == nil && resp.StatusCode/100 == 2
}

func (obj *DirectObject) Get() bool {
	req, _ := http.NewRequest("GET", obj.Url, nil)
	resp, err := client.Do(req)
	if resp != nil {
		io.Copy(ioutil.Discard, resp.Body)
	}
	return err == nil && resp.StatusCode/100 == 2
}

func (obj *DirectObject) Replicate() bool {
	req, _ := http.NewRequest("REPLICATE", obj.Url, nil)
	resp, err := client.Do(req)
	if resp != nil {
		io.Copy(ioutil.Discard, resp.Body)
	}
	return err == nil && resp.StatusCode/100 == 2
}

func (obj *DirectObject) Delete() bool {
	req, _ := http.NewRequest("DELETE", obj.Url, nil)
	req.Header.Set("X-Timestamp", hummingbird.GetTimestamp())
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	return err == nil && resp.StatusCode/100 == 2
}

func GetDevices(address string) []string {
	deviceUrl := fmt.Sprintf("%srecon/devices", address)
	req, err := http.NewRequest("GET", deviceUrl, nil)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(fmt.Sprintf("ERROR GETTING DEVICES: %s", err))
		os.Exit(1)
	}
	body, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	var rdata interface{}
	json.Unmarshal(body, &rdata)
	for _, v := range rdata.(map[string]interface{}) {
		retvals := []string{}
		for _, val := range v.([]interface{}) {
			retvals = append(retvals, val.(string))
		}
		return retvals
	}
	return []string{}
}

func RunDBench(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: [configuration file]")
		fmt.Println("The configuration file should look something like:")
		fmt.Println("    [dbench]")
		fmt.Println("    address = http://localhost:6010/")
		fmt.Println("    concurrency = 15")
		fmt.Println("    object_size = 131072")
		fmt.Println("    num_objects = 5000")
		fmt.Println("    num_gets = 30000")
		fmt.Println("    do_replicates = false")
		fmt.Println("    delete = yes")
		fmt.Println("    minimum_partition_number = 1000000000")
		os.Exit(1)
	}

	benchconf, err := hummingbird.LoadIniFile(args[0])
	if err != nil {
		fmt.Println("Error parsing ini file:", err)
		os.Exit(1)
	}

	address := benchconf.GetDefault("dbench", "address", "http://localhost:6010/")
	if !strings.HasSuffix(address, "/") {
		address = address + "/"
	}
	concurrency := int(benchconf.GetInt("dbench", "concurrency", 16))
	objectSize := benchconf.GetInt("dbench", "object_size", 131072)
	numObjects := benchconf.GetInt("dbench", "num_objects", 5000)
	numGets := benchconf.GetInt("dbench", "num_gets", 30000)
	doReplicates := benchconf.GetBool("dbench", "do_replicates", false)
	numPartitions := int64(100)
	minPartition := benchconf.GetInt("dbench", "minimum_partition_number", 1000000000)
	delete := benchconf.GetBool("dbench", "delete", true)

	deviceList := GetDevices(address)

	data := make([]byte, objectSize)
	objects := make([]DirectObject, numObjects)
	deviceParts := make(map[string]bool)
	for i, _ := range objects {
		device := deviceList[i%len(deviceList)]
		part := rand.Int63()%numPartitions+minPartition
		objects[i].Url = fmt.Sprintf("%s%s/%d/%s/%s/%d", address, device, part, "a", "c", rand.Int63())
		objects[i].Data = data

		deviceParts[fmt.Sprintf("%s/%d", device, part)] = true
	}

	work := make([]func() bool, len(objects))
	for i, _ := range objects {
		work[i] = objects[i].Put
	}
	DoJobs("PUT", work, concurrency)

	time.Sleep(time.Second * 2)

	replWork := make([]func() bool, 0)
	for replKey := range(deviceParts) {
		devicePart := strings.Split(replKey, "/")
		replWork = append(replWork, (&DirectObject{Url: fmt.Sprintf("%s%s/%s", address, devicePart[0], devicePart[1])}).Replicate)
	}
	if doReplicates {
		DoJobs("REPLICATE", replWork, concurrency)
	}

	work = make([]func() bool, numGets)
	for i := int64(0); i < numGets; i++ {
		work[i] = objects[int(rand.Int63()%int64(len(objects)))].Get
	}
	DoJobs("GET", work, concurrency)

	if delete {
		work = make([]func() bool, len(objects))
		for i, _ := range objects {
			work[i] = objects[i].Delete
		}
		DoJobs("DELETE", work, concurrency)
	}

	if doReplicates {
		DoJobs("REPLICATE", replWork, concurrency)
	}

}

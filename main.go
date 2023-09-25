package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path"

	"github.com/Filecoin-Titan/titan/api"
	"github.com/Filecoin-Titan/titan/api/client"
	"github.com/Filecoin-Titan/titan/api/types"
	cliutil "github.com/Filecoin-Titan/titan/cli/util"
	"github.com/filecoin-project/go-jsonrpc"
)

func main() {
	// 定义命令行参数
	locatorURL := flag.String("locator-url", "https://localhost:5000/rpc/v0", "locator url")
	apiKey := flag.String("api-key", "", "api key")

	// 解析命令行参数
	flag.Parse()

	if len(*locatorURL) == 0 {
		fmt.Println("locator-url can not empty")
		return
	}

	if len(*apiKey) == 0 {
		fmt.Println("api-key can not empty")
		return
	}

	// 获取其他非命令行参数
	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("please input file path")
		return
	}

	if err := execUpload(*apiKey, *locatorURL, args[0]); err != nil {
		fmt.Println("upload file error ", err.Error())
		return
	}

}

func execUpload(apiKey, locatorURL, filePath string) error {
	close, schedulerAPI, err := newSchedulerAPI(locatorURL, apiKey)
	if err != nil {
		return err
	}
	defer close()

	fileType := "file"
	if fileInfo, err := os.Stat(filePath); err != nil {
		return err
	} else if fileInfo.IsDir() {
		fileType = "folder"
	}

	tempFile := path.Join(os.TempDir(), path.Base(filePath))
	if _, err := os.Stat(tempFile); err == nil {
		os.Remove(tempFile)
	}

	root, err := createCar(filePath, tempFile)
	if err != nil {
		return err
	}

	if err := uploadFile(schedulerAPI, tempFile, root, path.Base(filePath), fileType); err != nil {
		return err
	}

	if err := os.Remove(tempFile); err != nil {
		return err
	}
	return nil
}

func uploadFile(schedulerAPI api.Scheduler, carFilePath, carCID, fileName, fileType string) error {
	f, err := os.Open(carFilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	fileInfo, err := f.Stat()
	if err != nil {
		return err
	}

	assetProperty := &types.AssetProperty{AssetCID: carCID, AssetName: fileName, AssetSize: fileInfo.Size(), AssetType: fileType}

	rsp, err := schedulerAPI.CreateUserAsset(context.Background(), assetProperty)
	if err != nil {
		fmt.Printf("CreateUserAsset error %#v\n", err)
		return fmt.Errorf("CreateUserAsset error %w", err)
	}

	if rsp.AlreadyExists {
		return fmt.Errorf("asset %s already exist", carCID)
	}

	err = uploadFileWithForm(carFilePath, rsp.UploadURL, rsp.Token)
	if err != nil {
		// fmt.Println("uploadFileWithForm error ", err.Error())
		return fmt.Errorf("uploadFileWithForm error %w", err)
	}

	return nil
}

func newSchedulerAPI(locatorURL, apiKey string) (func(), api.Scheduler, error) {
	udpPacketConn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return nil, nil, fmt.Errorf("ListenPacket %w", err)
	}

	// use http3 client
	httpClient, err := cliutil.NewHTTP3Client(udpPacketConn, true, "")
	if err != nil {
		return nil, nil, fmt.Errorf("NewHTTP3Client %w", err)
	}

	locatorAPI, _, err := client.NewLocator(context.TODO(), locatorURL, nil, jsonrpc.WithHTTPClient(httpClient))
	if err != nil {
		return nil, nil, fmt.Errorf("NewLocator %w", err)
	}

	schedulerURL, err := locatorAPI.GetSchedulerWithAPIKey(context.Background(), apiKey)
	if err != nil {
		return nil, nil, fmt.Errorf("GetSchedulerWithAPIKey %w", err)
	}

	headers := http.Header{}
	headers.Add("Authorization", "Bearer "+apiKey)

	schedulerAPI, apiClose, err := client.NewScheduler(context.TODO(), schedulerURL, headers, jsonrpc.WithHTTPClient(httpClient))
	if err != nil {
		return nil, nil, fmt.Errorf("NewScheduler %w", err)
	}

	close := func() {
		apiClose()
		udpPacketConn.Close()
	}
	return close, schedulerAPI, nil
}

func uploadFileWithForm(filePath, uploadURL, token string) error {
	// Open the file you want to upload
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	// Create a new multipart form body
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create a new form field for the file
	fileField, err := writer.CreateFormFile("file", stat.Name())
	if err != nil {
		return err
	}

	// Copy the file data to the form field
	_, err = io.Copy(fileField, file)
	if err != nil {
		return err
	}

	// Close the multipart form
	err = writer.Close()
	if err != nil {
		return err
	}

	// bar := progressbar.Default(stat.Size())
	totalSize := body.Len()
	dongSize := int64(0)
	pr := &ProgressReader{body, func(r int64) {
		if r > 0 {
			dongSize += r
			fmt.Printf("progress %d/%d\n", dongSize, totalSize)
		} else {
			fmt.Println("upload complete")
		}
	}}

	// Create a new HTTP request with the form data
	request, err := http.NewRequest("POST", uploadURL, pr)
	if err != nil {
		return fmt.Errorf("new request error %s", err.Error())
	}

	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)

	// Create an HTTP client and send the request
	client := http.DefaultClient
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("do error %s", err.Error())
	}
	defer response.Body.Close()

	// Check the response status
	fmt.Println("Response status:", response.Status)

	b, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return err
	}

	fmt.Println("Response body:", string(b))

	return nil
}

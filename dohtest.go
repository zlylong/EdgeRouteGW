package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
)

type DoHResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func main() {
	proxyURL, _ := url.Parse("socks5://127.0.0.1:10808")
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	
	req, _ := http.NewRequest("GET", "https://1.1.1.1/dns-query?name=technews.tw&type=A", nil)
	req.Header.Set("Accept", "application/dns-json")
	
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer resp.Body.Close()
	
	body, _ := ioutil.ReadAll(resp.Body)
	var dohResp DoHResponse
	json.Unmarshal(body, &dohResp)
	
	fmt.Printf("Status: %d\n", dohResp.Status)
	for _, a := range dohResp.Answer {
		if a.Type == 1 {
			fmt.Printf("IP: %s\n", a.Data)
		}
	}
}

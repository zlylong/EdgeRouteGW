package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

func main() {
	serverAddr := "127.0.0.1"
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			fmt.Printf("Go resolver intended to query: %s, but we redirect to %s:53\n", address, serverAddr)
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", net.JoinHostPort(serverAddr, "53"))
		},
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10 * time.Second)
	defer cancel()
	
	domain := "technews.tw"
	fmt.Printf("Resolving %s...\n", domain)
	addrs, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Success: %v\n", addrs)
	}
}

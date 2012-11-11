package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

var buffSize, _ = strconv.Atoi(os.Getenv("BUFF_SIZE"))
var wait, _ = strconv.Atoi(os.Getenv("WAIT"))
var logplexURL = os.Getenv("LOGPLEX_URL")
var socket = flag.String("socket", "", "Location of UNIX domain socket.")
var logplexToken = flag.String("logplex-token", "abc123", "Secret logplex token.")

func prepare(w io.Writer, batch []string) {
	for _, msg := range batch {
		t := time.Now().UTC().Format(time.RFC3339 + " ")
		//http://tools.ietf.org/html/rfc5424
		//<prival>version time host procid msgid msg \n
		line := "<0>1 " + t + "1234 " + *logplexToken + " web.1 " + "- - " + msg
		fmt.Fprintf(w, "%d %s", len(line), line)
	}
}

func outlet(batches <-chan []string) {
	for batch := range batches {
		u, err := url.Parse(logplexURL)
		if err != nil {
			log.Fatal("can't parse logplexURL")
		}
		u.User = url.UserPassword("", *logplexToken)
		var b bytes.Buffer
		prepare(&b, batch)
		req, _ := http.NewRequest("POST", u.String(), &b)
		req.Header.Add("Content-Type", "application/logplex-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("error=%v\n", err)
		} else {
			fmt.Printf("at=logplex-post status=%v\n", resp.StatusCode)
			resp.Body.Close()
		}
	}
}

func handle(lines <-chan string, batches chan<- []string) {
	ticker := time.Tick(time.Millisecond * time.Duration(wait))
	messages := make([]string, 0, buffSize)
	for {
		select {
		case <-ticker:
			if len(messages) > 0 {
				batches <- messages
				messages = make([]string, 0, buffSize)
			}
		case l := <-lines:
			messages = append(messages, l)
			if len(messages) == cap(messages) {
				batches <- messages
				messages = make([]string, 0, buffSize)
			}
		}
	}
}

func read(r io.ReadCloser, lines chan<- string, drops, reads chan<- uint64) {
	rdr := bufio.NewReader(r)
	for {
		line, err := rdr.ReadString('\n')
		//Drop the line if the lines buffer is full.
		//Set buffSize to reduce drops.
		if err == nil {
			select {
			case lines <- line:
				reads <- 1
			default:
				drops <- 1
			}
		} else {
			r.Close()
			return
		}
	}
}

func report(lines chan string, batches chan []string, drops, reads <-chan uint64) {
	for _ = range time.Tick(time.Second) {
		fmt.Fprintf(os.Stdout, "reads=%d drops=%d lines=%d batches=%d\n", <-reads, <-drops, len(lines), len(batches))
	}
}

func counter(n uint64) (chan<- uint64, <-chan uint64) {
	add, poll := make(chan uint64), make(chan uint64)
	go func() {
		for {
			select {
			case k := <-add:
				n += k
			case poll <- n:
				n = 0
			}
		}
	}()
	return add, poll
}

func main() {
	flag.Parse()

	//TODO Require a good cert from Logplex.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	http.DefaultTransport = tr

	dropadd, droppoll := counter(0)
	readadd, readpoll := counter(0)

	batches := make(chan []string)
	lines := make(chan string, buffSize)

	go report(lines, batches, droppoll, readpoll)
	go handle(lines, batches)
	go outlet(batches)

	if len(*socket) == 0 {
		read(os.Stdin, lines, dropadd, readadd)
	} else {
		l, err := net.Listen("unix", *socket)
		if err != nil {
			log.Fatal(err)
		}
		for {
			conn, err := l.Accept()
			if err != nil {
				fmt.Printf("Accept error. err=%v", err)
			}
			go read(conn, lines, dropadd, readadd)
		}
	}
}

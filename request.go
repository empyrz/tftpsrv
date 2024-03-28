package tftpsrv

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strconv"
	"time"
)

type Request struct {
	Filename    string        // The requested filename.
	Mode        string        // The request mode. Can usually be ignored.
	addr        *net.UDPAddr // Peer address
	ackChannel  chan uint16  // Channel which receives acknowledgement numbers.
	server      *Server
	blockSize   uint16
	blockBuf    bytes.Buffer
	blockNum    uint16
	terminated  bool
	options     map[string]string
}

// Add debug logging
func debugLog(format string, args ...interface{}) {
	log.Printf("[DEBUG] "+format, args...)
}

// Add error logging
func errorLog(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}

func (req *Request) setBlockSize(blockSize uint16) error {
	debugLog("Setting block size to: %d", blockSize)

	if blockSize < 8 || blockSize > 65464 {
		return fmt.Errorf("invalid block size: %d not in 8 <= blockSize <= 65464", blockSize)
	}

	req.blockSize = blockSize

	debugLog("Block size set successfully: %d", req.blockSize)

	return nil
}

func (req *Request) Write(data []byte) (int, error) {
	debugLog("Received write request. Data length: %d", len(data))

	n := 0
	for len(data) > 0 {
		nn, err := req.write(data)
		n += nn
		if err != nil {
			errorLog("Error writing data: %v", err)
			return n, err
		}
		data = data[nn:]
	}
	return n, nil
}

func (req *Request) write(data []byte) (int, error) {
	debugLog("Writing data block. Data length: %d", len(data))

	if req.terminated {
		errorLog("Request already terminated")
		return 0, ErrClosed
	}

	L := len(data)

	Lmax := int(req.blockSize) - req.blockBuf.Len()
	if L > Lmax {
		L = Lmax
	}

	if L > 0 {
		req.blockBuf.Write(data[0:L])
	}

	if req.blockBuf.Len() < int(req.blockSize) {
		debugLog("Data written successfully. Length: %d", L)
		return L, nil
	}

	err := req.flushBlock(false)
	if err != nil {
		errorLog("Error flushing block: %v", err)
		return 0, err
	}

	debugLog("Data written successfully. Length: %d", L)
	return L, nil
}

func (req *Request) flushBlock(isFinal bool) error {
	debugLog("Flushing data block. Is final: %t", isFinal)

	if req.blockBuf.Len() == 0 && !isFinal {
		debugLog("Empty block buffer. Nothing to flush.")
		return nil
	}

	if !isFinal && req.blockBuf.Len() != int(req.blockSize) {
		errorLog("Unexpected short block")
		return fmt.Errorf("unexpected short block")
	}

	err := req.sendAndWaitForAck(req.fbSendData, req.blockNum)
	if err != nil {
		errorLog("Error sending data block: %v", err)
		return err
	}

	req.blockBuf.Truncate(0)
	req.blockNum++

	debugLog("Data block flushed successfully")
	return nil
}

func (req *Request) fbSendData() error {
	debugLog("Sending data block. Block number: %d", req.blockNum)

	return req.server.sendTftpDataPacket(req.addr, req.blockNum, req.blockBuf.Bytes())
}

func (req *Request) sendAndWaitForAck(txFunc func() error, blockNum uint16) error {
	debugLog("Sending data and waiting for acknowledgment. Block number: %d", blockNum)

	t := time.Now()

loop:
	for {
		err := txFunc()
		if err != nil {
			errorLog("Error sending data: %v", err)
			return err
		}

		select {
		case brctl := <-req.ackChannel:
			if brctl == blockNum {
				debugLog("Acknowledgment received for block number: %d", blockNum)
				break loop
			}
		case <-time.After(req.server.RetransmissionTimeout):
			if time.Now().After(t.Add(req.server.RequestTimeout)) {
				req.terminate()
				errorLog("Request timed out")
				return ErrTimedOut
			}
		}
	}

	debugLog("Data sent and acknowledgment received successfully for block number: %d", blockNum)
	return nil
}

func (req *Request) terminate() {
	debugLog("Terminating request")

	delete(req.server.requests, req.name())
	req.terminated = true
}

func (req *Request) name() string {
	return nameFromAddr(req.addr)
}

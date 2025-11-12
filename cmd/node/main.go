package main

import (
	"context" // controls request timeouts and cancellations for gRPC calls
	"flag"    // for parsing command-line arguments
	"fmt"
	"log" // logging messages
	"math/rand"
	"net"     // For creating a TCP listener
	"strings" // To parse the peers string (A=localhost:5000,B=localhost:5001,C=localhost:5002)
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure" // // Used here because we’re only doing local testing

	pb "ricartagrawala/proto"
)

// Node implements the RAService defined in the proto
type Node struct {
	pb.UnimplementedRAServiceServer
	id    string                        // "A", "B" or "C"
	port  int                           // The port this node listens on
	peers map[string]pb.RAServiceClient // A map of peer IDs to gRPC client connections (pb.RAServiceClient)

	clock LamportClock

	// Ricart–Agrawala
	mu              sync.Mutex      // Protects all fields below
	requesting      bool            // True if node has requested CS
	inCS            bool            // True if node is currently in CS
	requestTS       int64           // Timestamp of this node's request
	replyCount      int             // How many REPLYs we've received
	deferredReplies map[string]bool // Peers whose replies are deferred
}

// --------------------------------------
// Manage Ricart Agrawala logic
// --------------------------------------

// RequestCriticalSection starts the Ricart Agrawala request process.
func (n *Node) RequestCriticalSection() {
	n.mu.Lock()
	if n.requesting || n.inCS {
		n.mu.Unlock()
		return // Already requesting or in CS
	}
	n.requesting = true
	n.replyCount = 0
	n.requestTS = n.clock.Tick()
	n.mu.Unlock()

	log.Printf("[%s] Requesting critical section (ts=%d)", n.id, n.requestTS)

	// Broadcast REQUEST to all peers
	for pid, client := range n.peers {
		go func(pid string, client pb.RAServiceClient) {
			req := &pb.Request{
				Timestamp: n.requestTS,
				FromId:    n.id,
			}
			_, err := client.RequestCS(
				context.Background(),
				req,
				grpc.WaitForReady(true), // Makes sure we wait until the connection is live
			)
			if err != nil {
				log.Printf("[%s] Failed to send RequestCS to %s: %v", n.id, pid, err)
			}
		}(pid, client)
	}
}

func (n *Node) TryEnterCriticalSection(totalPeers int) {
	n.mu.Lock()
	if n.replyCount != totalPeers-1 || !n.requesting {
		n.mu.Unlock()
		return
	}
	n.inCS = true
	n.requesting = false
	n.mu.Unlock()

	log.Printf("[%s] ENTERING critical section", n.id)
	time.Sleep(2 * time.Second)

	log.Printf("[%s] EXITING critical section", n.id)

	n.mu.Lock()
	n.inCS = false
	n.mu.Unlock()

	n.releaseDeferredReplies()
}

func (n *Node) releaseDeferredReplies() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for pid := range n.deferredReplies {
		go n.sendReply(pid)
	}
	n.deferredReplies = make(map[string]bool)
}

// sendReply sends a REPLY to a specific peer.
func (n *Node) sendReply(to string) {
	client, ok := n.peers[to]
	if !ok {
		log.Printf("[%s] Unknown peer %s for reply", n.id, to)
		return
	}

	ts := n.clock.Tick()
	rep := &pb.Reply{Timestamp: ts, FromId: n.id, ToId: to}

	_, err := client.SendReply(
		context.Background(),
		rep,
		grpc.WaitForReady(true), // Wait until ready instead of failing fast
	)
	if err != nil {
		log.Printf("[%s] Failed to send reply to %s: %v", n.id, to, err)
	} else {
		log.Printf("[%s] Sent REPLY(ts=%d) to %s", n.id, ts, to)
	}
}

// ---------------------------------------
// Implementation of proto methods
// ---------------------------------------

// RequestCS handles incoming requests from peers asking to enter critical section.
func (n *Node) RequestCS(ctx context.Context, req *pb.Request) (*pb.Empty, error) {
	n.clock.Receive(req.Timestamp)

	n.mu.Lock()
	shouldDefer := false
	if n.inCS {
		shouldDefer = true
	} else if n.requesting {
		// Defer if our request has higher priority, that is smaller ts or, on tie, smaller id)
		if n.requestTS < req.Timestamp || (n.requestTS == req.Timestamp && n.id < req.FromId) {
			shouldDefer = true
		}
	}
	if shouldDefer {
		if n.deferredReplies == nil {
			n.deferredReplies = make(map[string]bool)
		}
		n.deferredReplies[req.FromId] = true
		log.Printf("[%s] Deferring reply to %s", n.id, req.FromId)
	}
	n.mu.Unlock()

	if !shouldDefer {
		go n.sendReply(req.FromId)
	}
	return &pb.Empty{}, nil
}

// SendReply handles incoming replies (permission grants) from peers.
func (n *Node) SendReply(ctx context.Context, rep *pb.Reply) (*pb.Empty, error) {
	n.clock.Receive(rep.Timestamp)

	// Increment reply count
	n.mu.Lock()
	n.replyCount++
	totalPeers := len(n.peers) + 1 // Include self
	n.mu.Unlock()

	log.Printf("[%s] Received REPLY from %s (%d/%d)", n.id, rep.FromId, n.replyCount, totalPeers-1)

	// Check if we can enter Critical Section now
	n.TryEnterCriticalSection(totalPeers)
	return &pb.Empty{}, nil
}

// --------------------------
// Utility functions
// --------------------------

func startServer(node *Node, port int, ready chan<- struct{}) {
	lis, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Fatalf("[%s] Failed to listen: %v", node.id, err)
	}
	s := grpc.NewServer()
	pb.RegisterRAServiceServer(s, node)
	log.Printf("[%s] Listening on port %d", node.id, port)
	close(ready) // Tell main that we are ready to accept connections
	if err := s.Serve(lis); err != nil {
		log.Fatalf("[%s] gRPC server failed: %v", node.id, err)
	}
}

// connectToPeers iterates over all peer nodes (peerAddrs)
func connectToPeers(node *Node, peerAddrs map[string]string) {
	node.peers = make(map[string]pb.RAServiceClient)
	for pid, addr := range peerAddrs {
		if pid == node.id {
			continue
		}
		// Block until connected (with timeout) so first RPC won’t race the dial
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, err := grpc.DialContext(
			ctx,
			addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(), // <- Wait for TCP connect
		)
		if err != nil {
			log.Fatalf("[%s] Failed to connect to %s at %s: %v", node.id, pid, addr, err)
		}
		node.peers[pid] = pb.NewRAServiceClient(conn)
		log.Printf("[%s] Connected to peer %s (%s)", node.id, pid, addr)
	}
}

func main() {
	// -------------------------------
	// Parse CLI arguments
	// -------------------------------
	id := flag.String("id", "", "unique node ID")
	port := flag.Int("port", 0, "port to listen on")
	peersFlag := flag.String("peers", "", "comma-separated list: ID=address (e.g., A=127.0.0.1:5000,B=127.0.0.1:5001,C=127.0.0.1:5002)")
	flag.Parse()

	if *id == "" || *port == 0 || *peersFlag == "" {
		log.Fatalf("Usage: go run main.go -id A -port 5000 -peers A=127.0.0.1:5000,B=127.0.0.1:5001,C=127.0.0.1:5002")
	}

	// -------------------------------
	// Parse peer map
	// -------------------------------
	peerMap := make(map[string]string)
	pairs := strings.Split(*peersFlag, ",")
	for _, p := range pairs {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			log.Fatalf("Invalid peer format: %s", p)
		}
		peerMap[parts[0]] = parts[1]
	}

	// -------------------------------
	// Initialize node
	// -------------------------------
	node := &Node{
		id:              *id,
		port:            *port,
		deferredReplies: make(map[string]bool),
	}

	// -------------------------------
	// Start server
	// -------------------------------
	ready := make(chan struct{})
	go startServer(node, *port, ready)
	<-ready // Wait for the server to start listening
	log.Printf("[%s] Server is ready.", node.id)

	// -------------------------------
	// Connect to peers
	// -------------------------------
	connectToPeers(node, peerMap)
	log.Printf("[%s] Connected to all peers.", node.id)

	// -------------------------------
	// Periodic CS requests
	// -------------------------------
	go func() {
		rand.Seed(time.Now().UnixNano())
		initialDelay := time.Duration(rand.Intn(5000)) * time.Millisecond
		time.Sleep(initialDelay)

		for {
			delay := time.Duration(5+rand.Intn(5)) * time.Second
			time.Sleep(delay)
			node.RequestCriticalSection()
		}
	}()

	// -------------------------------
	// Periodic status report
	// -------------------------------
	go func() {
		for {
			time.Sleep(10 * time.Second)
			node.mu.Lock()
			log.Printf("[%s] status: inCS=%v, requesting=%v, replyCount=%d/%d, deferred=%v",
				node.id, node.inCS, node.requesting, node.replyCount, len(node.peers), node.deferredReplies)
			node.mu.Unlock()
		}
	}()

	select {} // Keep running
}

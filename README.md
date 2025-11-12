# Ricart–Agrawala Distributed Mutual Exclusion (Go + gRPC)

This project implements the **Ricart–Agrawala distributed mutual exclusion algorithm** using **Golang** and **gRPC**.

Each node runs as both a **gRPC server** and a **client**, and cooperates with other nodes to ensure that **only one node** can enter the critical section (CS) at a time — while maintaining liveness and fairness using **Lamport logical clocks**.

---

## Requirements

- **Go 1.20+**
- **Protocol Buffers** compiler (`protoc`)
- gRPC and protobuf Go plugins:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```



---

## Clone the repository


---

## Run the system 

### Terminal 1
  ```bash
  go run ./cmd/node/ -id A -port 5000 -peers A=127.0.0.1:5000,B=127.0.0.1:5001,C=127.0.0.1:5002
```
### Terminal 2
  ```bash
  go run ./cmd/node/ -id B -port 5001 -peers A=127.0.0.1:5000,B=127.0.0.1:5001,C=127.0.0.1:5002
  ```

### Terminal 3
  ```bash
  go run ./cmd/node/ -id C -port 5002 -peers A=127.0.0.1:5000,B=127.0.0.1:5001,C=127.0.0.1:5002
  ```

package worker

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"time"

	"github.com/fatedier/fft/pkg/log"
	"github.com/fatedier/fft/pkg/msg"
)

type Options struct {
	ServerAddr     string
	BindAddr       string
	AdvicePublicIP string

	LogFile    string
	LogLevel   string
	LogMaxDays int64
}

func (op *Options) Check() error {
	if op.LogMaxDays <= 0 {
		op.LogMaxDays = 3
	}
	return nil
}

type Service struct {
	serverAddr     string
	advicePublicIP string

	l         net.Listener
	matchCtl  *MatchController
	tlsConfig *tls.Config
}

func NewService(options Options) (*Service, error) {
	if err := options.Check(); err != nil {
		return nil, err
	}

	logway := "file"
	if options.LogFile == "console" {
		logway = "console"
	}
	log.InitLog(logway, options.LogFile, options.LogLevel, options.LogMaxDays)

	l, err := net.Listen("tcp", options.BindAddr)
	if err != nil {
		return nil, err
	}
	log.Info("fftw listen on: %s", l.Addr().String())

	return &Service{
		serverAddr:     options.ServerAddr,
		advicePublicIP: options.AdvicePublicIP,

		l:         l,
		matchCtl:  NewMatchController(),
		tlsConfig: generateTLSConfig(),
	}, nil
}

func (svc *Service) Run() error {
	go svc.worker()

	// connect to server
	conn, err := net.Dial("tcp", svc.serverAddr)
	if err != nil {
		return err
	}
	conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true})

	_, portStr, err := net.SplitHostPort(svc.l.Addr().String())
	if err != nil {
		return fmt.Errorf("get bind port error, bind address: %v", svc.l.Addr().String())
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("get bind port error")
	}

	register := NewRegister(int64(port), svc.advicePublicIP, svc.serverAddr)
	err = register.Register(conn)
	if err != nil {
		return fmt.Errorf("register worker to server error: %v", err)
	}
	log.Info("register to server success")

	register.RunKeepAlive(conn)
	return nil
}

func (svc *Service) worker() error {
	for {
		conn, err := svc.l.Accept()
		if err != nil {
			return err
		}
		conn = tls.Server(conn, svc.tlsConfig)
		go svc.handleConn(conn)
	}
}

func (svc *Service) handleConn(conn net.Conn) {
	var (
		rawMsg msg.Message
		err    error
	)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if rawMsg, err = msg.ReadMsg(conn); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	switch m := rawMsg.(type) {
	case *msg.NewSendFileStream:
		log.Debug("new send file stream [%s]", m.ID)
		tc := NewTransferConn(m.ID, conn, true)
		if err = svc.matchCtl.DealTransferConn(tc, 20*time.Second); err != nil {
			msg.WriteMsg(conn, &msg.NewSendFileStreamResp{
				Error: err.Error(),
			})
			conn.Close()
		}
	case *msg.NewReceiveFileStream:
		log.Debug("new recv file stream [%s]", m.ID)
		tc := NewTransferConn(m.ID, conn, false)
		if err = svc.matchCtl.DealTransferConn(tc, 20*time.Second); err != nil {
			msg.WriteMsg(conn, &msg.NewReceiveFileStreamResp{
				Error: err.Error(),
			})
			conn.Close()
		}
	case *msg.Ping:
		log.Debug("return pong to server ping")
		msg.WriteMsg(conn, &msg.Pong{})
		conn.Close()
		return
	default:
		conn.Close()
		return
	}
}

// Setup a bare-bones TLS config for the server
func generateTLSConfig() *tls.Config {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		panic(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{tlsCert}}
}

package langserver

import (
	"context"
	"fmt"

	"net"

	"github.com/livegrep/livegrep/server/config"
	"github.com/sourcegraph/jsonrpc2"
	"path/filepath"
	"time"
)

type ClientCapabilities struct{}

type ServerCapabilities struct{}

type InitializeParams struct {
	ProcessId        *int               `json:"processId"`
	OriginalRootPath string             `json:"originalRootPath"`
	RootPath         string             `json:"rootPath"`
	RootUri          string             `json:"rootUri"`
	Capabilities     ClientCapabilities `json:"capabilities"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

func GetLangServerFromFileExt(repo *config.RepoConfig, filePath string) *config.LangServer {
	fileExt := filepath.Ext(filePath)
	for _, langServer := range repo.LangServers {
		for _, ext := range langServer.Extensions {
			if ext == fileExt {
				return &langServer
			}
		}
	}
	return nil
}

type Client interface {
	Initialize(params InitializeParams) (InitializeResult, error)
	JumpToDef(params *TextDocumentPositionParams) ([]Location, error)
	Hover(params *TextDocumentPositionParams) (HoverResponse, error)
}

type langServerClientImpl struct {
	rpcClient *jsonrpc2.Conn
	ctx       context.Context
}

func CreateLangServerClient(address string) (client Client, err error) {
	ctx := context.Background()
	codec := jsonrpc2.VSCodeObjectCodec{}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return
	}
	rpcConn := jsonrpc2.NewConn(ctx, jsonrpc2.NewBufferedStream(conn, codec), nil)
	client = &langServerClientImpl{
		rpcClient: rpcConn,
		ctx:       ctx,
	}
	return client, nil
}

func (ls *langServerClientImpl) Initialize(params InitializeParams) (result InitializeResult, err error) {
	err = ls.invoke("initialize", params, &result)
	if err != nil {
		ls.invoke("initialized", nil, nil)
	}
	return
}

func (ls *langServerClientImpl) JumpToDef(params *TextDocumentPositionParams) (result []Location, err error) {
	err = ls.invoke("textDocument/definition", params, &result)
	return
}

func (ls *langServerClientImpl) Hover(
	params *TextDocumentPositionParams,
) (result HoverResponse, err error) {
	err = ls.invoke("textDocument/hover", params, result)
	return
}

func (ls *langServerClientImpl) invoke(method string, params interface{}, result interface{}) error {
	start := time.Now()
	err := ls.rpcClient.Call(ls.ctx, method, params, &result)
	fmt.Printf("%s returned in %s\nParams: %+v, Result: %+v, err: %+v\n", method, time.Since(start),
		params, result, err)
	return err
}

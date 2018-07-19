package langserver

import (
	"context"
	"net"

	"github.com/livegrep/livegrep/server/config"
	"github.com/livegrep/livegrep/server/log"
	"github.com/sourcegraph/jsonrpc2"
	"path/filepath"
	"time"
)

// infers a language server for a given file. Picks only one.
func ForFile(repo *config.RepoConfig, filePath string) *config.LangServer {
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
	Initialize(ctx context.Context, params *InitializeParams) (InitializeResult, error)
	JumpToDef(ctx context.Context, params *TextDocumentPositionParams) ([]Location, error)
	Hover(ctx context.Context, params *TextDocumentPositionParams) (HoverResponse, error)
}

type langServerClientImpl struct {
	rpcClient *jsonrpc2.Conn
}

func NewClient(ctx context.Context, address string) (client Client, err error) {
	codec := jsonrpc2.VSCodeObjectCodec{}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return
	}
	rpcConn := jsonrpc2.NewConn(ctx, jsonrpc2.NewBufferedStream(conn, codec), nil)
	client = &langServerClientImpl{
		rpcClient: rpcConn,
	}
	return
}

func (ls *langServerClientImpl) Initialize(ctx context.Context, params *InitializeParams) (result InitializeResult, err error) {
	err = ls.invoke(ctx, "initialize", params, &result)
	if err != nil {
		ls.invoke(ctx, "initialized", nil, nil)
	}
	return
}

func (ls *langServerClientImpl) JumpToDef(
	ctx context.Context,
	params *TextDocumentPositionParams,
) (result []Location, err error) {
	err = ls.invoke(ctx, "textDocument/definition", params, &result)
	return
}

func (ls *langServerClientImpl) Hover(
	ctx context.Context,
	params *TextDocumentPositionParams,
) (result HoverResponse, err error) {
	err = ls.invoke(ctx, "textDocument/hover", params, result)
	return
}

func (ls *langServerClientImpl) invoke(ctx context.Context, method string, params interface{}, result interface{}) error {
	start := time.Now()
	err := ls.rpcClient.Call(ctx, method, params, &result)
	log.Printf(ctx, "%s %s\nParams: %+v, Result: %+v, err: %+v\n", method, time.Since(start), params, result, err)
	return err
}

@echo off
REM Script to generate gRPC code from protobuf definitions

echo Generating gRPC code...

REM Ensure the output directory exists
if not exist "protos\eventstore" mkdir protos\eventstore

REM Generate Go code with protoc
REM Make sure protoc and protoc-gen-go/protoc-gen-go-grpc are installed
REM Install with:
REM   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
REM   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

protoc --go_out=. --go_opt=paths=source_relative ^
       --go-grpc_out=. --go-grpc_opt=paths=source_relative ^
       protos/eventstore.proto

if errorlevel 1 (
    echo Failed to generate gRPC code
    echo.
    echo Please ensure you have:
    echo 1. protoc installed ^(download from https://github.com/protocolbuffers/protobuf/releases^)
    echo 2. protoc-gen-go installed ^(go install google.golang.org/protobuf/cmd/protoc-gen-go@latest^)
    echo 3. protoc-gen-go-grpc installed ^(go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest^)
    exit /b 1
)

echo Successfully generated gRPC code

@echo off
REM Phase 0 gate script (PLAN_V7 section 4 / section 23)
set PATH=%PATH%;C:\Program Files\Go\bin
go test ./... || goto :fail
go vet ./... || goto :fail
go build ./cmd/Free-Model-Router || goto :fail
echo GATE_OK
exit /b 0
:fail
exit /b 1

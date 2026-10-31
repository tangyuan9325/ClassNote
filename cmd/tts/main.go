// tts 用 Windows SAPI 合成语音到 WAV：tts <文本> <输出.wav>
package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: tts <text> <out.wav>")
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole.CoInitialize(0)
	defer ole.CoUninitialize()

	voice, err := oleutil.CreateObject("SAPI.SpVoice")
	if err != nil {
		fmt.Println("SpVoice:", err)
		return
	}
	defer voice.Release()
	v, _ := voice.QueryInterface(ole.IID_IDispatch)

	stream, err := oleutil.CreateObject("SAPI.SpFileStream")
	if err != nil {
		fmt.Println("SpFileStream:", err)
		return
	}
	defer stream.Release()
	st, _ := stream.QueryInterface(ole.IID_IDispatch)
	// 3 = SSFMCreateForWrite
	if _, err := oleutil.CallMethod(st, "Open", os.Args[2], int32(3), false); err != nil {
		fmt.Println("open:", err)
		return
	}
	if _, err := oleutil.PutPropertyRef(v, "AudioOutputStream", st); err != nil {
		fmt.Println("set stream:", err)
		return
	}
	if _, err := oleutil.CallMethod(v, "Speak", os.Args[1]); err != nil {
		fmt.Println("speak:", err)
		return
	}
	oleutil.CallMethod(st, "Close")
	fi, err := os.Stat(os.Args[2])
	if err != nil {
		fmt.Println("no output:", err)
		return
	}
	fmt.Println("saved", os.Args[2], fi.Size(), "bytes")
}

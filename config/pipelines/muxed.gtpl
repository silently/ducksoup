appsrc name=audio_src is-live=true format=GST_FORMAT_TIME
appsrc name=video_src is-live=true format=GST_FORMAT_TIME min-latency=33333333
appsink name=audio_sink qos=true
appsink name=video_sink qos=true
{{/* always record dry */}}
{{.Video.Muxer}} name=dry_recorder ! filesink location=data/{{.Namespace}}/{{.FilePrefix}}-dry.{{.Video.Extension}}
{{/* record fx if one on audio or video */}}
{{if or .Video.Fx .Audio.Fx }}
    {{.Video.Muxer}} name=wet_recorder ! filesink location=data/{{.Namespace}}/{{.FilePrefix}}-wet.{{.Video.Extension}}
{{end}}

audio_src. !
{{.Audio.Rtp.Caps}} ! 
{{.Audio.Rtp.JitterBuffer}} ! 
{{if .Audio.Fx}}
    {{.Audio.Rtp.Depay}} !

    tee name=tee_audio_in ! 
        queue ! 
        dry_recorder.

    tee_audio_in. ! 
        queue ! 
        {{.Audio.Decoder}} !
        audioconvert !
        audio/x-raw,channels=1 !
        {{.Audio.Fx}} ! 
        audioconvert ! 
        {{.Audio.EncodeWith "audio_encoder_wet" .Namespace .FilePrefix}} ! 

        tee name=tee_audio_out ! 
            queue ! 
            wet_recorder.

        tee_audio_out. ! 
            queue ! 
            {{.Audio.Rtp.Pay}} !
            audio_sink.
{{else}}
    tee name=tee_audio_in ! 
        queue ! 
        {{.Audio.Rtp.Depay}} !
        {{/* audio stream has to be written to two files if there is a video fx*/}}
        {{if .Video.Fx }}
            tee name=tee_audio_out !
                queue ! 
                dry_recorder.

            tee_audio_out. !
                queue ! 
                wet_recorder.
        {{else}}
            dry_recorder.
        {{end}}

    tee_audio_in. ! 
        queue ! 
        audio_sink.
{{end}}

video_src. !
{{.Video.Rtp.Caps}} ! 
{{.Video.Rtp.JitterBuffer}} ! 
{{if .Video.Fx}}
    {{.Video.Rtp.Depay}} ! 
    h264timestamper !

    tee name=tee_video_in ! 
        queue ! 
        dry_recorder.

    tee_video_in. ! 
        queue ! 
        {{.Video.Decoder}} !
        {{.Video.ConstraintFormatFramerate .Framerate}} !

        videoconvert ! 
        {{.Video.Fx}} !
        {{if .Video.Overlay }}
            timeoverlay time-mode=1 ! 
        {{end}}

        queue ! 
        {{.Video.ConstraintFormat}} !
        {{.Video.EncodeWith "video_encoder_wet" .Namespace .FilePrefix}} ! 

        tee name=tee_video_out ! 
            queue ! 
            wet_recorder.

        tee_video_out. ! 
            queue ! 
            {{.Video.Rtp.Pay}} ! 
            video_sink.
{{else}}
    tee name=tee_video_in ! 
        queue ! 
        {{.Video.Rtp.Depay}} ! 
        h264timestamper !
        
        {{/* video stream has to be written to two files if there is an aufio fx*/}}
        {{if .Audio.Fx }}
            tee name=tee_video_out !
                queue ! 
                dry_recorder.

            tee_video_out. !
                queue ! 
                wet_recorder.
        {{else}}
            dry_recorder.
        {{end}}

    tee_video_in. ! 
        queue ! 
        video_sink.
{{end}}

#include <stdio.h>
#include <time.h>
#include <gst/app/gstappsrc.h>

#include "gst.h"

#define GST_VIDEO_EVENT_FORCE_KEY_UNIT_NAME "GstForceKeyUnit"
#define GST_RTP_EVENT_RETRANSMISSION_REQUEST "GstRTPRetransmissionRequest"

GMainLoop *gstreamer_main_loop = NULL;
void gstreamer_start_mainloop(void)
{
    gstreamer_main_loop = g_main_loop_new(NULL, FALSE);

    g_main_loop_run(gstreamer_main_loop);
}

void stop_pipeline(GstElement* pipeline) {
    // use previously set name as id
    char *id = gst_element_get_name(pipeline);

    gst_element_set_state(pipeline, GST_STATE_NULL);
    gst_object_unref(pipeline);

    goStopCallback(id);
}

static gboolean gstreamer_bus_call(GstBus *bus, GstMessage *msg, gpointer data)
{
    GstElement* pipeline = (GstElement*) data;
    switch (GST_MESSAGE_TYPE(msg))
    {
    case GST_MESSAGE_EOS: {
        stop_pipeline(pipeline);
        break;
    }
    case GST_MESSAGE_ERROR:
    {
        gchar *debug;
        GError *error;

        g_printerr ("[error] [gst.c] from element %s: %s\n",
            GST_OBJECT_NAME (msg->src), error->message);
        g_printerr ("[error] [gst.c] debugging information: %s\n", debug ? debug : "none");

        g_free(debug);
        g_error_free(error);

        stop_pipeline(pipeline);
        break;
    }
    default:
        //g_print("got message %s\n", gst_message_type_get_name (GST_MESSAGE_TYPE (msg)));
        break;
    }

    return TRUE;
}

GstElement *gstreamer_parse_pipeline(char *pipelineStr, char *id)
{
    gst_init(NULL, NULL);
    GError *error = NULL;
    GstElement *pipeline = gst_parse_launch(pipelineStr, &error);

    // use element name to store id (used when C calls go on new samples to reference what pipeline is involved)
    gst_element_set_name(pipeline, id);

    return pipeline;
}


GstFlowReturn gstreamer_new_audio_sample(GstElement *object, gpointer data)
{
    GstSample *sample = NULL;
    GstBuffer *buffer = NULL;
    gpointer copy = NULL;
    gsize copy_size = 0;
    GstElement *pipeline = (GstElement*) data;

    // use previously set name as id
    char *id = gst_element_get_name(pipeline);

    g_signal_emit_by_name(object, "pull-sample", &sample);
    if (sample)
    {
        buffer = gst_sample_get_buffer(sample);
        if (buffer)
        {
            gst_buffer_extract_dup(buffer, 0, gst_buffer_get_size(buffer), &copy, &copy_size);
            goAudioCallback(id, copy, copy_size, GST_BUFFER_PTS(buffer));
        }
        gst_sample_unref(sample);
    }

    return GST_FLOW_OK;
}

GstFlowReturn gstreamer_new_video_sample(GstElement *object, gpointer data)
{
    GstSample *sample = NULL;
    GstBuffer *buffer = NULL;
    gpointer copy = NULL;
    gsize copy_size = 0;
    GstElement *pipeline = (GstElement*) data;

    // use previously set name as id
    char *id = gst_element_get_name(pipeline);

    g_signal_emit_by_name(object, "pull-sample", &sample);
    if (sample)
    {
        buffer = gst_sample_get_buffer(sample);
        if (buffer)
        {
            gst_buffer_extract_dup(buffer, 0, gst_buffer_get_size(buffer), &copy, &copy_size);
            goVideoCallback(id, copy, copy_size, GST_BUFFER_PTS(buffer));
        }
        gst_sample_unref(sample);
    }

    return GST_FLOW_OK;
}


// TODO use <gst/video/video.h> implementation
gboolean gst_event_is (GstEvent * event, const gchar * name)
{
  const GstStructure *s;

  g_return_val_if_fail (event != NULL, FALSE);

  if (GST_EVENT_TYPE (event) != GST_EVENT_CUSTOM_UPSTREAM)
    return FALSE;               /* Not a force key unit event */

  s = gst_event_get_structure (event);
  if (s == NULL || !gst_structure_has_name (s, name))
    return FALSE;

  return TRUE;
}

// credits to https://github.com/cryptagon/ion-cluster
// This pad probe will get triggered when UPSTREAM events get fired on the appsrc.  
// We use this to listen for GstEventForceKeyUnit, and forward that to the go binding to request a PLI
static GstPadProbeReturn gstreamer_input_track_event_pad_probe_cb(GstPad * pad, GstPadProbeInfo * info, gpointer data)
{
    GstEvent *event = GST_PAD_PROBE_INFO_EVENT(info);
    GstElement *pipeline = (GstElement*) data;

    // use previously set name as id
    char *id = gst_element_get_name(pipeline);

    if (gst_event_is (event, GST_VIDEO_EVENT_FORCE_KEY_UNIT_NAME)) {
        g_print ("[info] [gst.c] pad_probe got upstream forceKeyUnit\n");
        goPLICallback(id);
    }
    // else if (gst_event_is (event, GST_RTP_EVENT_RETRANSMISSION_REQUEST)) {
    //     // TODO handle as a nack and possibly disable pion nack interceptor
    //     g_print ("[info] [gst.c] pad_probe got upstream RTP transmission request\n");
    // }
    return GST_PAD_PROBE_OK;
}

void gstreamer_start_pipeline(GstElement *pipeline)
{
    GstBus *bus = gst_pipeline_get_bus(GST_PIPELINE(pipeline));
    gst_bus_add_watch(bus, gstreamer_bus_call, pipeline);
    gst_object_unref(bus);
    // src
    GstElement *video_src = gst_bin_get_by_name(GST_BIN(pipeline), "video_src");
    GstPad *video_src_pad = gst_element_get_static_pad(video_src, "src");
    gst_pad_add_probe (video_src_pad, GST_PAD_PROBE_TYPE_EVENT_UPSTREAM, gstreamer_input_track_event_pad_probe_cb, pipeline, NULL);
    gst_object_unref(video_src);
    gst_object_unref(video_src_pad);
    // sinks
    GstElement *audio_sink = gst_bin_get_by_name(GST_BIN(pipeline), "audio_sink");
    GstElement *video_sink = gst_bin_get_by_name(GST_BIN(pipeline), "video_sink");
    g_object_set(audio_sink, "emit-signals", TRUE, NULL);
    g_signal_connect(audio_sink, "new-sample", G_CALLBACK(gstreamer_new_audio_sample), pipeline);
    gst_object_unref(audio_sink);
    g_object_set(video_sink, "emit-signals", TRUE, NULL);
    g_signal_connect(video_sink, "new-sample", G_CALLBACK(gstreamer_new_video_sample), pipeline);
    gst_object_unref(video_sink);
    // buffer request pad
    GstElement *audio_buffer = gst_bin_get_by_name(GST_BIN(pipeline), "audio_buffer");
    GstElement *video_buffer = gst_bin_get_by_name(GST_BIN(pipeline), "video_buffer");

    // TODO push_rtcp does not work
    // TODO deprecated gst_element_get_request_pad https://gitlab.freedesktop.org/gstreamer/gst-docs/-/merge_requests/152
    // update when GStreamer 1.20 is out
    // GstPad *audio_rtcp_pad = gst_element_get_request_pad (audio_buffer, "sink_rtcp");
    // GstPad *video_rtcp_pad = gst_element_get_request_pad (video_buffer, "sink_rtcp");
    // gst_pad_activate_mode (audio_rtcp_pad, GST_PAD_MODE_PULL, TRUE);
    // gst_pad_activate_mode (video_rtcp_pad, GST_PAD_MODE_PULL, TRUE);
    // gst_object_unref(audio_buffer);
    // gst_object_unref(video_buffer);
    // gst_object_unref(audio_rtcp_pad);
    // gst_object_unref(video_rtcp_pad);


    gst_element_set_state(pipeline, GST_STATE_PLAYING);
}

void gstreamer_stop_pipeline(GstElement *pipeline)
{
    // query GstStateChangeReturn within 0.1s, if GST_STATE_CHANGE_ASYNC, sending an EOS will fail main loop
    GstStateChangeReturn changeReturn = gst_element_get_state(pipeline, NULL, NULL, 100000000);

    // use previously set name as id
    char *id = gst_element_get_name(pipeline);

    if(changeReturn == GST_STATE_CHANGE_ASYNC) {
        // force stop
        stop_pipeline(pipeline);
    } else {
        // gracefully stops media recording
        gst_element_send_event(pipeline, gst_event_new_eos());
    }
}

void gstreamer_push_buffer(char *name, GstElement *pipeline, void *buffer, int len)
{
    GstElement *src = gst_bin_get_by_name(GST_BIN(pipeline), name);
    
    if (src != NULL)
    {
        gpointer p = g_memdup(buffer, len);
        GstBuffer *buffer = gst_buffer_new_wrapped(p, len);
        gst_app_src_push_buffer(GST_APP_SRC(src), buffer);
        gst_object_unref(src);
    }
}

void gstreamer_push_rtcp_buffer(char *name, GstElement *pipeline, void *buffer, int len)
{
    GstElement *src = gst_bin_get_by_name(GST_BIN(pipeline), name);
    GstPad *rtcp_sink_pad = gst_element_get_static_pad(src, "sink_rtcp");
    gst_object_unref(src);

    g_print(">>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\n");
    
    if (rtcp_sink_pad != NULL)
    {
        g_print("<<<<<<<<<<<<<<<\n");
        gpointer p = g_memdup(buffer, len);
        GstBuffer *buffer = gst_buffer_new_wrapped(p, len);
        gst_pad_pull_range (rtcp_sink_pad, 0, len, &buffer);
        gst_object_unref(rtcp_sink_pad);
    }

}

float gstreamer_get_property_float(GstElement *pipeline, char *name, char *prop) {
    GstElement* el;
    gfloat value;
 
    el = gst_bin_get_by_name(GST_BIN(pipeline), name);
    
    if(el) {
        g_object_get(el, prop, &value, NULL);
        gst_object_unref(el);
    }

    return value;
}

void gstreamer_set_property_float(GstElement *pipeline, char *name, char *prop, float value)
{
    GstElement* el;

    el = gst_bin_get_by_name(GST_BIN(pipeline), name);
    
    if(el) {
        g_object_set(el, prop, value, NULL);
        gst_object_unref(el);
    }
}

gint gstreamer_get_property_int(GstElement *pipeline, char *name, char *prop) {
    GstElement* el;
    gint value;
 
    el = gst_bin_get_by_name(GST_BIN(pipeline), name);
    
    if(el) {
        g_object_get(el, prop, &value, NULL);
        gst_object_unref(el);
    }

    return value;
}

void gstreamer_set_property_int(GstElement *pipeline, char *name, char *prop, gint value)
{
    GstElement* el;

    el = gst_bin_get_by_name(GST_BIN(pipeline), name);
    
    if(el) {
        g_object_set(el, prop, value, NULL);
        gst_object_unref(el);
    }
}
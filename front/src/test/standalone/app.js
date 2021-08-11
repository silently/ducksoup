// State
const state = {
  audioIn: null,
};

// Config
const FRONT_PREFIX = '/test/standalone/';

const DEFAULT_CONSTRAINTS = {
  video: {
    width: { ideal: 800 },
    height: { ideal: 600 },
    frameRate: { ideal: 30 },
    facingMode: { ideal: "user" },
  },
  audio: {
    sampleSize: 16,
    autoGainControl: false,
    channelCount: 1,
    latency: { ideal: 0.003 },
    echoCancellation: false,
    noiseSuppression: false,
  },
};

const DEFAULT_PEER_CONFIGURATION = {
  iceServers: [
    {
      urls: "stun:stun.l.google.com:19302",
    },
  ],
};

const getQueryVariable = (key) => {
  const query = window.location.search.substring(1);
  const vars = query.split("&");
  for (let i = 0; i < vars.length; i++) {
    const pair = vars[i].split("=");
    if (decodeURIComponent(pair[0]) == key) {
      return decodeURIComponent(pair[1]);
    }
  }
};

const init = async () => {
  // Init state
  const userId = getQueryVariable("userId");
  const roomId = getQueryVariable("roomId");
  const size = parseInt(getQueryVariable("size"), 10);
  const audioFx = getQueryVariable("audioFx").replace("+", " ");
  const videoFx = getQueryVariable("videoFx").replace("+", " ");
  if (!roomId || !userId) window.location.href = FRONT_PREFIX;
  window.history.replaceState({}, document.title, `${FRONT_PREFIX}live/`);
  state.userId = userId;
  state.roomId = roomId;
  state.size = size;
  state.audioFx = audioFx;
  state.videoFx = videoFx;
  // Init UX
  try {
    const devices = await navigator.mediaDevices.enumerateDevices();
    const audioSelect = document.getElementById("audio-select");
    for (let i = 0; i !== devices.length; ++i) {
      const device = devices[i];
      if (device.kind === "audioinput") {
        const li = document.createElement("li");
        const a = document.createElement("a");
        a.classList.add("dropdown-item");
        a.href = "#";
        a.text = device.label || `microphone ${audioInputSelect.length + 1}`;
        a.addEventListener("click", () => {
          state.audioIn = device.deviceId;
          document.getElementById("audio-in-label").textContent = device.label;
        });
        li.appendChild(a);
        audioSelect.appendChild(li);
      }
    }
  } catch (err) {
    console.error(err);
  }
};

const forceMozillaMono = (sdp) => {
  if (!window.navigator.userAgent.includes("Mozilla")) return sdp;
  return sdp
    .split("\r\n")
    .map((line) => {
      if (line.startsWith("a=fmtp:111")) {
        return line.replace("stereo=1", "stereo=0");
      } else {
        return line;
      }
    })
    .join("\r\n");
};

const processSDP = (sdp) => {
  const output = forceMozillaMono(sdp);
  return output;
};

const randomId = () => Math.random().toString(36).replace(/[^a-z]+/g, '').substr(0, 8);

const startRTC = async () => {
  // UX
  document.getElementById("start").classList.add("d-none");
  document.getElementById("stop").classList.remove("d-none");

  // RTCPeerConnection
  const pc = new RTCPeerConnection(DEFAULT_PEER_CONFIGURATION);
  // Add local tracks before signaling
  const constraints = { ...DEFAULT_CONSTRAINTS };
  if (state.audioIn) {
    constraints.audio = {
      ...constraints.audio,
      deviceId: { ideal: state.audioIn },
    };
  }
  const stream = await navigator.mediaDevices.getUserMedia(constraints);
  const localVideoEl = document.getElementById("local-video");
  localVideoEl.srcObject = stream;
  stream.getTracks().forEach((track) => pc.addTrack(track, stream));

  // Signaling
  const wsProtocol = window.location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${wsProtocol}://${window.location.host}/ws`);

  ws.onopen = function () {
    const { roomId, size, audioFx, videoFx } = state;
    console.log(state)
    ws.send(
      JSON.stringify({
        kind: "join",
        payload: JSON.stringify({
          userId: randomId(),
          roomId,
          size,
          videoFx,
          audioFx,
          namespace: "standalone" }),
      })
    );
  };

  ws.onclose = function () {
    console.log("[ws] closed");
  };

  ws.onerror = function (event) {
    console.error("[ws] " + event.data);
  };

  ws.onmessage = async function (event) {
    let message = JSON.parse(event.data);
    if (!message) return console.error("failed to parse message");

    const { kind, payload } = message;

    if (kind === "offer") {
      const offer = JSON.parse(payload);
      if (!offer) {
        return console.error("failed to parse answer");
      }
      pc.setRemoteDescription(offer);
      const answer = await pc.createAnswer();
      answer.sdp = processSDP(answer.sdp);
      pc.setLocalDescription(answer);
      ws.send(
        JSON.stringify({
          kind: "answer",
          payload: JSON.stringify(answer),
        })
      );
    } else if (kind === "candidate") {
      const candidate = JSON.parse(payload);
      if (!candidate) {
        return console.error("failed to parse candidate");
      }
      pc.addIceCandidate(candidate);
    } else if (kind === "ending") {
      document.getElementById("ending").classList.remove("d-none");
    } else if (kind === "end") {
      window.location.href = `${FRONT_PREFIX}end/`;
    } else if (kind === "error-full") {
      window.location.href = `${FRONT_PREFIX}full/`;
    } 
  };

  pc.onicecandidate = (e) => {
    if (!e.candidate) return;
    ws.send(
      JSON.stringify({
        kind: "candidate",
        payload: JSON.stringify(e.candidate),
      })
    );
  };

  pc.ontrack = function (event) {
    let el = document.createElement(event.track.kind);
    el.id = event.track.id;
    el.srcObject = event.streams[0];
    el.autoplay = true;
    document.getElementById("placeholder").appendChild(el);

    event.streams[0].onremovetrack = ({ track }) => {
      const el = document.getElementById(track.id);
      if (el) el.parentNode.removeChild(el);
    };
  };

  // Stats
  setInterval(() => logStats(pc), 1000);
};

let now = Date.now();
let audioBytesSent = 0;
let audioBytesReceived = 0;
let videoBytesSent = 0;
let videoBytesReceived = 0;

// Stats
const kbps = (bytes, duration, intro) => {
  const result = (8 * bytes) / duration / 1024;
  return result.toFixed(1);
};
const logStats = async (pc) => {
  const pcStats = await pc.getStats();
  const newNow = Date.now();
  let newAudioBytesSent = 0;
  let newAudioBytesReceived = 0;
  let newVideoBytesSent = 0;
  let newVideoBytesReceived = 0;

  pcStats.forEach((report) => {
    if (report.type === "outbound-rtp" && report.kind === "audio") {
      newAudioBytesSent += report.bytesSent;
    } else if (report.type === "inbound-rtp" && report.kind === "audio") {
      newAudioBytesReceived += report.bytesReceived;
    } else if (report.type === "outbound-rtp" && report.kind === "video") {
      newVideoBytesSent += report.bytesSent;
    } else if (report.type === "inbound-rtp" && report.kind === "video") {
      newVideoBytesReceived += report.bytesReceived;
    }
  });

  const elapsed = (newNow - now) / 1000;
  document.getElementById("audio-up").textContent = kbps(
    newAudioBytesSent - audioBytesSent,
    elapsed
  );
  document.getElementById("audio-down").textContent = kbps(
    newAudioBytesReceived - audioBytesReceived,
    elapsed
  );
  document.getElementById("video-up").textContent = kbps(
    newVideoBytesSent - videoBytesSent,
    elapsed
  );
  document.getElementById("video-down").textContent = kbps(
    newVideoBytesReceived - videoBytesReceived,
    elapsed
  );
  now = newNow;
  audioBytesSent = newAudioBytesSent;
  audioBytesReceived = newAudioBytesReceived;
  videoBytesSent = newVideoBytesSent;
  videoBytesReceived = newVideoBytesReceived;

  // for (const sender of pc.getSenders()) {
  //   console.log("---------- RTCRtpSender stat", sender.track.kind);
  //   const senderStats = await sender.getStats();
  //   senderStats.forEach((report) => {
  //     console.log(report.type, report);
  //   });
  // }
};

document.addEventListener("DOMContentLoaded", init);
// UX
document.addEventListener("DOMContentLoaded", () => {
  document.getElementById("start").addEventListener("click", startRTC);
  document
    .getElementById("stop")
    .addEventListener("click", () => location.reload());
});

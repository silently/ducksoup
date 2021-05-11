(()=>{var y=Object.defineProperty;var v=Object.prototype.hasOwnProperty;var f=Object.getOwnPropertySymbols,h=Object.prototype.propertyIsEnumerable;var m=(e,o,n)=>o in e?y(e,o,{enumerable:!0,configurable:!0,writable:!0,value:n}):e[o]=n,l=(e,o)=>{for(var n in o||(o={}))v.call(o,n)&&m(e,n,o[n]);if(f)for(var n of f(o))h.call(o,n)&&m(e,n,o[n]);return e};var d={audioIn:null},I={video:{width:{ideal:640},height:{ideal:480},frameRate:{ideal:30},facingMode:{ideal:"user"}},audio:{sampleSize:16,autoGainControl:!1,channelCount:1,latency:{ideal:.003},echoCancellation:!1,noiseSuppression:!1}},S={iceServers:[{urls:"stun:stun.l.google.com:19302"}]},u=e=>{let n=window.location.search.substring(1).split("&");for(let s=0;s<n.length;s++){let i=n[s].split("=");if(decodeURIComponent(i[0])==e)return decodeURIComponent(i[1])}},D=async()=>{await navigator.mediaDevices.getUserMedia({audio:!0,video:!0});let e=await navigator.mediaDevices.enumerateDevices(),o=document.getElementById("audio-source"),n=document.getElementById("video-source");for(let s=0;s!==e.length;++s){let i=e[s],t=document.createElement("option");t.value=i.deviceId,i.kind==="audioinput"?(t.text=i.label||`microphone ${o.length+1}`,o.appendChild(t)):i.kind==="videoinput"&&(t.text=i.label||`camera ${n.length+1}`,n.appendChild(t))}},E=e=>{window.parent&&window.parent.postMessage(e,window.location.origin)},N=async()=>{let e=u("room"),o=u("name"),n=u("proc"),s=parseInt(u("duration"),10),i=u("uid"),t=u("aid"),a=u("vid");if(typeof e=="undefined"||typeof o=="undefined"||!["0","1"].includes(n)||isNaN(s)||typeof i=="undefined")document.getElementById("placeholder").innerHTML="Invalid parameters";else{d=l(l({},d),{room:e,name:o,proc:n,duration:s,uid:i,audioDeviceId:t,videoDeviceId:a});try{await D(),await C()}catch(r){console.error(r),p("error")}}},O=e=>window.navigator.userAgent.includes("Mozilla")?e.split(`\r
`).map(o=>o.startsWith("a=fmtp:111")?o.replace("stereo=1","stereo=0"):o).join(`\r
`):e,T=e=>O(e),p=e=>{d.stream.getTracks().forEach(o=>o.stop()),E(e)},C=async()=>{let e=new RTCPeerConnection(S),o=l({},I);d.audioDeviceId&&(o.audio=l(l({},o.audio),{deviceId:{ideal:d.audioDeviceId}})),d.videoDeviceId&&(o.video=l(l({},o.video),{deviceId:{ideal:d.videoDeviceId}}));let n=await navigator.mediaDevices.getUserMedia(o);n.getTracks().forEach(t=>e.addTrack(t,n)),d.stream=n;let s=window.location.protocol==="https:"?"wss":"ws",i=new WebSocket(`${s}://${window.location.host}/ws`);i.onopen=function(){let{room:t,name:a,proc:r,duration:c,uid:g}=d,w=Boolean(parseInt(r));i.send(JSON.stringify({type:"join",payload:JSON.stringify({room:t,name:a,duration:c,uid:g,proc:w})}))},i.onclose=function(t){console.log("[ws] closed"),p("disconnected")},i.onerror=function(t){console.error("[ws] error: "+t.data),p("error")},i.onmessage=async function(t){let a=JSON.parse(t.data);if(!a)return console.error("[ws] can't parse message");if(a.type==="offer"){let r=JSON.parse(a.payload);if(!r)return console.error("[ws] can't parse offer");console.log("[ws] received offer"),e.setRemoteDescription(r);let c=await e.createAnswer();c.sdp=T(c.sdp),e.setLocalDescription(c),i.send(JSON.stringify({type:"answer",payload:JSON.stringify(c)}))}else if(a.type==="candidate"){let r=JSON.parse(a.payload);if(!r)return console.error("[ws] can't parse candidate");console.log("[ws] candidate"),e.addIceCandidate(r)}else a.type==="start"?console.log("[ws] start"):a.type==="finishing"?(console.log("[ws] finishing"),document.getElementById("finishing").classList.remove("d-none")):(a.type.startsWith("error")||a.type==="finish")&&p(a.type)},e.onicecandidate=t=>{!t.candidate||i.send(JSON.stringify({type:"candidate",payload:JSON.stringify(t.candidate)}))},e.ontrack=function(t){let a=document.createElement(t.track.kind);a.id=t.track.id,a.srcObject=t.streams[0],a.autoplay=!0,document.getElementById("placeholder").appendChild(a),t.streams[0].onremovetrack=({track:r})=>{let c=document.getElementById(r.id);c&&c.parentNode.removeChild(c)}}};document.addEventListener("DOMContentLoaded",N);})();

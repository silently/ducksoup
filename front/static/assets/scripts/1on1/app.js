(()=>{var y=Object.defineProperty;var w=Object.prototype.hasOwnProperty;var f=Object.getOwnPropertySymbols,h=Object.prototype.propertyIsEnumerable;var m=(e,o,n)=>o in e?y(e,o,{enumerable:!0,configurable:!0,writable:!0,value:n}):e[o]=n,l=(e,o)=>{for(var n in o||(o={}))w.call(o,n)&&m(e,n,o[n]);if(f)for(var n of f(o))h.call(o,n)&&m(e,n,o[n]);return e};var d={audioIn:null},I={video:{width:{ideal:640},height:{ideal:480},frameRate:{ideal:30},facingMode:{ideal:"user"}},audio:{sampleSize:16,autoGainControl:!1,channelCount:1,latency:{ideal:.003},echoCancellation:!1,noiseSuppression:!1}},D={iceServers:[{urls:"stun:stun.l.google.com:19302"}]},u=e=>{let n=window.location.search.substring(1).split("&");for(let r=0;r<n.length;r++){let a=n[r].split("=");if(decodeURIComponent(a[0])==e)return decodeURIComponent(a[1])}},S=async()=>{await navigator.mediaDevices.getUserMedia({audio:!0,video:!0});let e=await navigator.mediaDevices.enumerateDevices(),o=document.getElementById("audio-source"),n=document.getElementById("video-source");for(let r=0;r!==e.length;++r){let a=e[r],t=document.createElement("option");t.value=a.deviceId,a.kind==="audioinput"?(t.text=a.label||`microphone ${o.length+1}`,o.appendChild(t)):a.kind==="videoinput"&&(t.text=a.label||`camera ${n.length+1}`,n.appendChild(t))}},k=e=>{window.parent&&window.parent.postMessage(e,window.location.origin)},C=async()=>{let e=u("room"),o=u("name"),n=u("proc"),r=parseInt(u("duration"),10),a=u("uid"),t=u("aid"),s=u("vid");if(typeof e=="undefined"||typeof o=="undefined"||!["0","1"].includes(n)||isNaN(r)||typeof a=="undefined")document.getElementById("placeholder").innerHTML="Invalid parameters";else{d=l(l({},d),{room:e,name:o,proc:n,duration:r,uid:a,audioDeviceId:t,videoDeviceId:s});try{await S(),await E()}catch(i){console.error(i),p()}}},N=e=>window.navigator.userAgent.includes("Mozilla")?e.split(`\r
`).map(o=>o.startsWith("a=fmtp:111")?o.replace("stereo=1","stereo=0"):o).join(`\r
`):e,O=e=>N(e),p=e=>{d.stream.getTracks().forEach(o=>o.stop()),k(e?"stop":"error")},E=async()=>{let e=new RTCPeerConnection(D),o=l({},I);d.audioDeviceId&&(o.audio=l(l({},o.audio),{deviceId:{ideal:d.audioDeviceId}})),d.videoDeviceId&&(o.video=l(l({},o.video),{deviceId:{ideal:d.videoDeviceId}}));let n=await navigator.mediaDevices.getUserMedia(o);n.getTracks().forEach(t=>e.addTrack(t,n)),d.stream=n;let r=window.location.protocol==="https:"?"wss":"ws",a=new WebSocket(`${r}://${window.location.host}/ws`);a.onopen=function(){let{room:t,name:s,proc:i,duration:c,uid:g}=d,v=Boolean(parseInt(i));a.send(JSON.stringify({type:"join",payload:JSON.stringify({room:t,name:s,duration:c,uid:g,proc:v})}))},a.onclose=function(t){console.log("Websocket has closed"),p()},a.onerror=function(t){console.error("ws: "+t.data),p()},a.onmessage=async function(t){let s=JSON.parse(t.data);if(!s)return console.error("failed to parse msg");switch(s.type){case"offer":{let i=JSON.parse(s.payload);if(!i)return console.error("failed to parse answer");e.setRemoteDescription(i);let c=await e.createAnswer();c.sdp=O(c.sdp),e.setLocalDescription(c),a.send(JSON.stringify({type:"answer",payload:JSON.stringify(c)}));break}case"candidate":{let i=JSON.parse(s.payload);if(!i)return console.error("failed to parse candidate");e.addIceCandidate(i);break}case"start":{let i=(d.duration-10)*1e3;i>0&&setTimeout(()=>{document.getElementById("finishing").classList.remove("d-none")},i);break}case"stop":{p(!0);break}case"error":{p();break}}},e.onicecandidate=t=>{!t.candidate||a.send(JSON.stringify({type:"candidate",payload:JSON.stringify(t.candidate)}))},e.ontrack=function(t){let s=document.createElement(t.track.kind);s.id=t.track.id,s.srcObject=t.streams[0],s.autoplay=!0,document.getElementById("placeholder").appendChild(s),t.streams[0].onremovetrack=({track:i})=>{let c=document.getElementById(i.id);c&&c.parentNode.removeChild(c)}}};document.addEventListener("DOMContentLoaded",C);})();

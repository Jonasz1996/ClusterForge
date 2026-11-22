// noVNC heeft geen eigen types; dit is het deel dat de console gebruikt.
declare module "@novnc/novnc/lib/rfb" {
  type Options = { credentials?: { password?: string }; wsProtocols?: string[]; shared?: boolean };
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: Options);
    scaleViewport: boolean;
    resizeSession: boolean;
    clipViewport: boolean;
    background: string;
    focusOnClick: boolean;
    viewOnly: boolean;
    disconnect(): void;
    sendCredentials(c: { password?: string }): void;
    sendCtrlAltDel(): void;
    sendKey(keysym: number, code: string | null, down?: boolean): void;
    focus(): void;
  }
}

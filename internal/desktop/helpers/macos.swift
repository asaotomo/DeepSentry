import AppKit
import ApplicationServices
import Foundation
import ImageIO
import UniformTypeIdentifiers

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data(message.utf8)); exit(1)
}
let input = FileHandle.standardInput.readDataToEndOfFile()
guard let r = try? JSONSerialization.jsonObject(with: input) as? [String: Any], let action = r["action"] as? String else { fail("Invalid helper request") }
let display = CGMainDisplayID()
let bounds = CGDisplayBounds(display)
func number(_ name: String) -> Int { (r[name] as? NSNumber)?.intValue ?? 0 }
func frontApp() -> NSRunningApplication? { NSWorkspace.shared.frontmostApplication }
func appID() -> String {
    guard let app = frontApp() else { return "" }
    if let id = app.bundleIdentifier, !id.isEmpty { return id }
    return "pid:\(app.processIdentifier)"
}
func foreground() -> String {
    guard let app = frontApp() else { return "" }
    let element = AXUIElementCreateApplication(app.processIdentifier)
    var window: CFTypeRef?
    var title: CFTypeRef?
    if AXUIElementCopyAttributeValue(element, kAXFocusedWindowAttribute as CFString, &window) == .success, let window {
        _ = AXUIElementCopyAttributeValue(window as! AXUIElement, kAXTitleAttribute as CFString, &title)
    }
    return "\(app.processIdentifier):\(title as? String ?? "")"
}
// Window numbers do not need Screen Recording permission and stay stable
// while a title changes, so they tell a new window of the same app apart.
func windowID() -> String {
    guard let app = frontApp() else { return "" }
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
    for info in list {
        guard (info[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == app.processIdentifier,
              (info[kCGWindowLayer as String] as? NSNumber)?.intValue == 0,
              let number = info[kCGWindowNumber as String] as? NSNumber else { continue }
        return number.stringValue
    }
    return ""
}
func copyAttribute(_ element: AXUIElement, _ name: CFString) -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(element, name, &value) == .success else { return nil }
    return value
}
func axString(_ element: AXUIElement, _ name: CFString) -> String {
    copyAttribute(element, name) as? String ?? ""
}
func axChildren(_ element: AXUIElement) -> [AXUIElement] {
    copyAttribute(element, kAXChildrenAttribute as CFString) as? [AXUIElement] ?? []
}
func clipName(_ raw: String) -> String {
    let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    if trimmed.count <= 80 { return trimmed }
    return String(trimmed.prefix(80))
}
func elementName(_ element: AXUIElement) -> String {
    for attr in [kAXTitleAttribute as CFString, kAXDescriptionAttribute as CFString] {
        let name = clipName(axString(element, attr))
        if !name.isEmpty { return name }
    }
    return ""
}
func axPressable(_ element: AXUIElement) -> Bool {
    var names: CFArray?
    guard AXUIElementCopyActionNames(element, &names) == .success, let names else { return false }
    return ((names as? [String]) ?? []).contains(kAXPressAction as String)
}
func axSettable(_ element: AXUIElement) -> Bool {
    var settable = DarwinBoolean(false)
    return AXUIElementIsAttributeSettable(element, kAXValueAttribute as CFString, &settable) == .success && settable.boolValue
}
func axFrame(_ element: AXUIElement) -> CGRect? {
    guard let position = copyAttribute(element, kAXPositionAttribute as CFString),
          let size = copyAttribute(element, kAXSizeAttribute as CFString),
          CFGetTypeID(position) == AXValueGetTypeID(),
          CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
    var point = CGPoint.zero
    var box = CGSize.zero
    guard AXValueGetValue(position as! AXValue, .cgPoint, &point), AXValueGetValue(size as! AXValue, .cgSize, &box) else { return nil }
    return CGRect(origin: point, size: box)
}
func focusedWindow() -> AXUIElement? {
    guard let app = frontApp() else { return nil }
    let axApp = AXUIElementCreateApplication(app.processIdentifier)
    var window: CFTypeRef?
    guard AXUIElementCopyAttributeValue(axApp, kAXFocusedWindowAttribute as CFString, &window) == .success, let window else { return nil }
    return (window as! AXUIElement)
}
func collectElements(from window: AXUIElement) -> [[String: Any]] {
    var out: [[String: Any]] = []
    func walk(_ element: AXUIElement, _ path: String, _ depth: Int) {
        if out.count >= 80 || depth > 6 { return }
        for (i, child) in axChildren(element).enumerated() {
            if out.count >= 80 { return }
            let childPath = path.isEmpty ? "\(i)" : "\(path)/\(i)"
            let role = axString(child, kAXRoleAttribute as CFString)
            let name = elementName(child)
            let press = axPressable(child)
            let settable = axSettable(child)
            if let frame = axFrame(child), frame.width > 0, frame.height > 0, !name.isEmpty || press || settable {
                out.append([
                    "index": out.count, "role": role, "name": name, "press": press, "settable": settable,
                    "x": Int(frame.minX), "y": Int(frame.minY), "w": Int(frame.width), "h": Int(frame.height), "path": childPath,
                ])
            }
            if depth < 6 { walk(child, childPath, depth + 1) }
        }
    }
    walk(window, "", 1)
    return out
}
func elementAt(_ path: String) -> AXUIElement? {
    guard let window = focusedWindow(), !path.isEmpty else { return nil }
    var current = window
    for part in path.split(separator: "/") {
        guard let index = Int(part) else { return nil }
        let kids = axChildren(current)
        if index < 0 || index >= kids.count { return nil }
        current = kids[index]
    }
    return current
}
func verify(_ element: AXUIElement) {
    let role = axString(element, kAXRoleAttribute as CFString)
    let name = elementName(element)
    if role != (r["expect_role"] as? String ?? "") || name != (r["expect_name"] as? String ?? "") {
        fail("element changed; observe again")
    }
}
if action == "request_permissions" {
    let options = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
    _ = AXIsProcessTrustedWithOptions(options)
    _ = CGRequestScreenCaptureAccess()
    exit(0)
}
let trusted = AXIsProcessTrusted()
let screen = CGPreflightScreenCaptureAccess()
if action == "status" {
    let state: [String: Any] = ["driver":"macos-coregraphics", "ready":trusted && screen,
      "reason":trusted && screen ? "" : "Enable Accessibility and Screen Recording for computer-use-helper / DeepSentry in System Settings, then restart. Permissions are not bypassed.",
      "x":Int(bounds.minX), "y":Int(bounds.minY), "width":Int(bounds.width), "height":Int(bounds.height),
      "foreground":foreground(), "app_id":appID(), "window_id":windowID(), "surface":"primary display"]
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject:state)); exit(0)
}
if action == "capture" {
    guard screen, let path = r["path"] as? String else { fail("Screen Recording permission or output path missing") }
    let task = Process(); task.executableURL = URL(fileURLWithPath:"/usr/sbin/screencapture")
    task.arguments = ["-x", "-D", "1", "-t", "png", path]
    try task.run(); task.waitUntilExit(); if task.terminationStatus != 0 { fail("screencapture failed") }; exit(0)
}
if action == "elements" {
    guard trusted else { fail("Accessibility permission missing") }
    let elements = focusedWindow().map(collectElements) ?? []
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject:["elements": elements])); exit(0)
}
guard trusted else { fail("Accessibility permission missing") }
func mouse(_ type: CGEventType, _ point: CGPoint, _ button: CGMouseButton, _ count: Int64 = 1) {
    guard let e = CGEvent(mouseEventSource:nil, mouseType:type, mouseCursorPosition:point, mouseButton:button) else { fail("Cannot allocate mouse event") }
    e.setIntegerValueField(.mouseEventClickState, value:count); e.post(tap:.cghidEventTap)
}
let point = CGPoint(x:number("x"),y:number("y"))
let button: CGMouseButton = (r["button"] as? String)=="right" ? .right : ((r["button"] as? String)=="middle" ? .center : .left)
let down: CGEventType = button == .right ? .rightMouseDown : (button == .center ? .otherMouseDown : .leftMouseDown)
let up: CGEventType = button == .right ? .rightMouseUp : (button == .center ? .otherMouseUp : .leftMouseUp)
let dragged: CGEventType = button == .right ? .rightMouseDragged : (button == .center ? .otherMouseDragged : .leftMouseDragged)
let keys: [String:CGKeyCode] = ["A":0,"S":1,"D":2,"F":3,"H":4,"G":5,"Z":6,"X":7,"C":8,"V":9,"B":11,"Q":12,"W":13,"E":14,"R":15,"Y":16,"T":17,"1":18,"2":19,"3":20,"4":21,"6":22,"5":23,"9":25,"7":26,"8":28,"0":29,"O":31,"U":32,"I":34,"P":35,"L":37,"J":38,"K":40,"N":45,"M":46,"ENTER":36,"TAB":48,"SPACE":49,"BACKSPACE":51,"ESCAPE":53,"DELETE":117,"HOME":115,"END":119,"PAGEUP":116,"PAGEDOWN":121,"LEFT":123,"RIGHT":124,"DOWN":125,"UP":126]
switch action {
case "move":mouse(.mouseMoved,point,button)
case "click", "double_click":
    let count = action == "double_click" ? 2 : 1
    for i in 1...count { mouse(down,point,button,Int64(i)); mouse(up,point,button,Int64(i)); if i < count { usleep(80000) } }
case "drag":
    mouse(down,point,button)
    let target = CGPoint(x:number("to_x"), y:number("to_y"))
    for i in 1...10 { let f = Double(i)/10;mouse(dragged,CGPoint(x:point.x+(target.x-point.x)*f,y:point.y+(target.y-point.y)*f),button);usleep(10000) }
    mouse(up,target,button)
case "scroll":
    if (r["has_point"] as? Bool) == true { mouse(.mouseMoved, point, .left) }
    guard let event = CGEvent(scrollWheelEvent2Source:nil,units:.line,wheelCount:1,wheel1:Int32(-number("amount")),wheel2:0,wheel3:0) else { fail("Cannot allocate scroll event") }
    event.post(tap:.cghidEventTap)
case "type":
    guard let text = r["text"] as? String else { fail("Missing text") }
    // Send Unicode directly; do not overwrite the user's clipboard.
    for scalar in text { let chars = Array(String(scalar).utf16)
        for pressed in [true,false] { do {
            guard let e=CGEvent(keyboardEventSource:nil,virtualKey:0,keyDown:pressed) else { fail("Cannot allocate Unicode event") }
            chars.withUnsafeBufferPointer { e.keyboardSetUnicodeString(stringLength:chars.count,unicodeString:$0.baseAddress!) };e.post(tap:.cghidEventTap)
        } }
    }
case "key":
    let parts = (r["key"] as? String ?? "").uppercased().split(separator:"+").map(String.init)
    guard let last=parts.last, let code=keys[last] else {fail("Unsupported key")}
    var flags: CGEventFlags = []
    for modifier in parts.dropLast() { switch modifier {case "CTRL":flags.insert(.maskControl);case "ALT":flags.insert(.maskAlternate);case "SHIFT":flags.insert(.maskShift);case "META":flags.insert(.maskCommand);default:fail("Invalid modifier")} }
    for pressed in [true,false] {
        guard let e=CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:pressed) else { fail("Cannot allocate keyboard event") }
        e.flags=flags;e.post(tap:.cghidEventTap)
    }
case "press_element":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    if AXUIElementPerformAction(element, kAXPressAction as CFString) != .success { fail("AXPress failed") }
case "click_element":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    mouse(down,point,button); mouse(up,point,button)
case "set_value":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    guard axSettable(element), let text = r["text"] as? String else { fail("element is not settable") }
    if AXUIElementSetAttributeValue(element, kAXValueAttribute as CFString, text as CFTypeRef) != .success { fail("AX set value failed") }
case "release":
    let current=CGEvent(source:nil)?.location ?? .zero
    mouse(.leftMouseUp,current,.left);mouse(.rightMouseUp,current,.right);mouse(.otherMouseUp,current,.center)
    for code: CGKeyCode in [55,56,58,59] {CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:false)?.post(tap:.cghidEventTap)}
default:fail("Unsupported action")
}

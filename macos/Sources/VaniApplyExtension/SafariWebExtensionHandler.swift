import Foundation
import SafariServices
import VaniApplyKit

// Safari's native side of the extension: browser.runtime.sendNativeMessage
// from the extension's background script lands here (content scripts cannot
// reach it). Same protocol and store as the Chrome host. The sandbox cannot
// start `vani`, so the synchronous draft op answers "unsupported"; drafts and
// suggestions go to the Vani Apply agent as jobs over XPC instead, and so do the
// Jobs ops (jobFind/jobMark), which XPCAgentClient answers as a JobsBackend.

@objc(SafariWebExtensionHandler)
final class SafariWebExtensionHandler: NSObject, NSExtensionRequestHandling {
    func beginRequest(with context: NSExtensionContext) {
        let item = context.inputItems.first as? NSExtensionItem
        let message = item?.userInfo?[SFExtensionMessageKey]

        let reply: [String: Any]
        if let message {
            do {
                reply = NativeProtocol.handle(message, store: try BankStore.shared(), source: .safari, agent: XPCAgentClient())
            } catch {
                reply = ["error": String(describing: error)]
            }
        } else {
            reply = ["error": "no message"]
        }

        let response = NSExtensionItem()
        response.userInfo = [SFExtensionMessageKey: reply]
        context.completeRequest(returningItems: [response], completionHandler: nil)
    }
}

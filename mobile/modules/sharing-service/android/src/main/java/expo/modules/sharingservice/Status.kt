package expo.modules.sharingservice

import java.util.concurrent.CopyOnWriteArrayList

/** In-process state shared between the service and the Expo module (for UI events). */
object Status {
  @Volatile var running = false
  @Volatile var connected = false
  @Volatile var lastError: String? = null

  val listeners = CopyOnWriteArrayList<() -> Unit>()
  fun changed() = listeners.forEach { it() }
}

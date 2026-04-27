package com.github.kr328.clash.service.clash.module

import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.os.PowerManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.getSystemService
import com.github.kr328.clash.common.compat.getColorCompat
import com.github.kr328.clash.common.compat.pendingIntentFlags
import com.github.kr328.clash.common.constants.Components
import com.github.kr328.clash.common.constants.Intents
import com.github.kr328.clash.common.util.ticker
import com.github.kr328.clash.core.Clash
import com.github.kr328.clash.core.model.ProxySort
import com.github.kr328.clash.service.R
import com.github.kr328.clash.service.StatusProvider
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.selects.select
import java.util.concurrent.TimeUnit

class DynamicNotificationModule(service: Service) : Module<Unit>(service) {
    private val builder = NotificationCompat.Builder(service, StaticNotificationModule.CHANNEL_ID)
        .setSmallIcon(R.drawable.ic_logo_service)
        .setOngoing(true)
        .setColor(service.getColorCompat(R.color.color_clash))
        .setOnlyAlertOnce(true)
        .setShowWhen(false)
        .setContentTitle("Not Selected")
        .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
        .setContentIntent(
            PendingIntent.getActivity(
                service,
                R.id.nf_clash_status,
                Intent().setComponent(Components.MAIN_ACTIVITY)
                    .setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP),
                pendingIntentFlags(PendingIntent.FLAG_UPDATE_CURRENT)
            )
        )

    private val notificationManager = NotificationManagerCompat.from(service)

    // Resolves full proxy chain starting from groupName
    // Result example: "PROXY → 🚀 Best Ping → 🇫🇮 Финляндия 44"
    private fun resolveChain(startGroup: String, maxDepth: Int = 4): String {
        val parts = mutableListOf(startGroup)
        var current = startGroup

        repeat(maxDepth) {
            val group = try {
                Clash.queryGroup(current, ProxySort.Default)
            } catch (e: Exception) {
                return parts.joinToString(" → ")
            }

            // group.now is the currently selected proxy in this group
            val selected = group.now
            if (selected.isNullOrBlank() || selected == current) {
                return parts.joinToString(" → ")
            }

            // Check if selected is itself a group (has sub-selection)
            val subGroup = try {
                Clash.queryGroup(selected, ProxySort.Default)
            } catch (e: Exception) {
                // Not a group, it's a direct proxy — add and stop
                parts.add(selected)
                return parts.joinToString(" → ")
            }

            parts.add(selected)

            if (subGroup.now.isNullOrBlank() || subGroup.now == selected) {
                // Leaf group or direct proxy
                return parts.joinToString(" → ")
            }

            current = selected
        }

        return parts.joinToString(" → ")
    }

    private fun update() {
        val chain = try {
            resolveChain("PROXY")
        } catch (e: Exception) {
            "PROXY"
        }

        val notification = builder
            .setContentText(chain)
            .build()

        notificationManager.notify(R.id.nf_clash_status, notification)
    }

    override suspend fun run() = coroutineScope {
        var shouldUpdate = service.getSystemService<PowerManager>()?.isInteractive ?: true

        val screenToggle = receiveBroadcast(false, Channel.CONFLATED) {
            addAction(Intent.ACTION_SCREEN_ON)
            addAction(Intent.ACTION_SCREEN_OFF)
        }

        val profileLoaded = receiveBroadcast(capacity = Channel.CONFLATED) {
            addAction(Intents.ACTION_PROFILE_LOADED)
        }

        val ticker = ticker(TimeUnit.SECONDS.toMillis(1))

        while (true) {
            select<Unit> {
                screenToggle.onReceive {
                    when (it.action) {
                        Intent.ACTION_SCREEN_ON ->
                            shouldUpdate = true
                        Intent.ACTION_SCREEN_OFF ->
                            shouldUpdate = false
                    }
                }
                profileLoaded.onReceive {
                    builder.setContentTitle(StatusProvider.currentProfile ?: "Not selected")
                }
                if (shouldUpdate) {
                    ticker.onReceive {
                        update()
                    }
                }
            }
        }
    }
}

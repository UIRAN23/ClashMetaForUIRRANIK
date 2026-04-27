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
import com.github.kr328.clash.core.model.Proxy
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

    data class ChainNode(val name: String, val delay: Int)

    // Получить delay конкретного прокси из группы.
    // ProxyGroup.proxies — только первые 50 (SliceProxyList).
    // Чтобы найти выбранный прокси:
    // 1. Сначала ищем в уже загруженном списке (Default sort)
    // 2. Если не нашли — запрашиваем ещё раз с сортировкой Delay:
    //    у url-test группы лучший (выбранный) прокси всегда будет первым при сортировке по задержке
    private fun getDelayForProxy(groupName: String, proxyName: String): Int {
        // Попытка 1: искать в дефолтном списке (первые 50)
        val defaultGroup = Clash.queryGroup(groupName, ProxySort.Default)
        val fromDefault = defaultGroup.proxies.find { it.name == proxyName }?.delay
        if (fromDefault != null && fromDefault > 0) return fromDefault

        // Попытка 2: сортировка по Delay — выбранный прокси будет в топе
        return try {
            val delayGroup = Clash.queryGroup(groupName, ProxySort.Delay)
            delayGroup.proxies.find { it.name == proxyName }?.delay ?: 0
        } catch (e: Exception) {
            0
        }
    }

    private fun resolveChain(startGroup: String, maxDepth: Int = 4): String {
        val nodes = mutableListOf<ChainNode>()
        var current = startGroup
        var parentGroup = startGroup

        nodes.add(ChainNode(startGroup, 0))

        repeat(maxDepth) {
            val group = Clash.queryGroup(current, ProxySort.Default)

            if (group.type == Proxy.Type.Unknown || group.now.isBlank() || group.now == current) {
                return formatChain(nodes)
            }

            val selected = group.now
            parentGroup = current

            // Проверяем является ли selected тоже группой
            val subGroup = Clash.queryGroup(selected, ProxySort.Default)
            val isLeaf = subGroup.type == Proxy.Type.Unknown || subGroup.now.isBlank()

            val delay = if (isLeaf) {
                // Конечный прокси — получаем его delay из родительской группы
                getDelayForProxy(parentGroup, selected)
            } else {
                0
            }

            nodes.add(ChainNode(selected, delay))

            if (isLeaf) {
                return formatChain(nodes)
            }

            current = selected
        }

        return formatChain(nodes)
    }

    private fun formatChain(nodes: List<ChainNode>): String {
        if (nodes.isEmpty()) return "PROXY"

        return nodes.mapIndexed { index, node ->
            val isLast = index == nodes.size - 1
            if (isLast && node.delay > 0) "${node.name} (${node.delay}ms)" else node.name
        }.joinToString(" → ")
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
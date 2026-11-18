package com.agentmux.mobile.ui.theme

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.unit.dp

/**
 * Icons the core Material set lacks, drawn from Material Icons path data
 * (Apache License 2.0) on the usual 24-unit grid.
 */
object AppIcons {
    val Chat: ImageVector by lazy {
        icon("Chat", "M20,2L4,2c-1.1,0 -2,0.9 -2,2v18l4,-4h14c1.1,0 2,-0.9 2,-2L22,4c0,-1.1 -0.9,-2 -2,-2zM20,16L6,16l-2,2L4,4h16v12z")
    }
    val Agent: ImageVector by lazy {
        icon(
            "Agent",
            "M20,9V7c0,-1.1 -0.9,-2 -2,-2h-3c0,-1.66 -1.34,-3 -3,-3S9,3.34 9,5H6c-1.1,0 -2,0.9 -2,2v2c-1.66,0 -3,1.34 -3,3" +
                "s1.34,3 3,3v4c0,1.1 0.9,2 2,2h12c1.1,0 2,-0.9 2,-2v-4c1.66,0 3,-1.34 3,-3S21.66,9 20,9zM7.5,11.5" +
                "C7.5,10.67 8.17,10 9,10s1.5,0.67 1.5,1.5S9.83,13 9,13S7.5,12.33 7.5,11.5zM16,17H8v-2h8V17zM15,13" +
                "c-0.83,0 -1.5,-0.67 -1.5,-1.5S14.17,10 15,10s1.5,0.67 1.5,1.5S15.83,13 15,13z",
        )
    }
    val Devices: ImageVector by lazy {
        icon(
            "Devices",
            "M4,6h18V4H4c-1.1,0 -2,0.9 -2,2v11H0v3h14v-3H4V6zM23,8h-6c-0.55,0 -1,0.45 -1,1v10c0,0.55 0.45,1 1,1h6" +
                "c0.55,0 1,-0.45 1,-1V9c0,-0.55 -0.45,-1 -1,-1zM22,17h-4v-7h4v7z",
        )
    }
    val Computer: ImageVector by lazy {
        icon("Computer", "M20,18c1.1,0 1.99,-0.9 1.99,-2L22,6c0,-1.1 -0.9,-2 -2,-2H4c-1.1,0 -2,0.9 -2,2v10c0,1.1 0.9,2 2,2H0v2h24v-2h-4zM4,6h16v10H4V6z")
    }
    val Phone: ImageVector by lazy {
        icon("Phone", "M17,1.01L7,1c-1.1,0 -2,0.9 -2,2v18c0,1.1 0.9,2 2,2h10c1.1,0 2,-0.9 2,-2V3c0,-1.1 -0.9,-1.99 -2,-1.99zM17,19H7V5h10v14z")
    }
    val Code: ImageVector by lazy {
        icon("Code", "M9.4,16.6L4.8,12l4.6,-4.6L8,6l-6,6 6,6 1.4,-1.4zM14.6,16.6l4.6,-4.6 -4.6,-4.6L16,6l6,6 -6,6 -1.4,-1.4z")
    }
    val Scan: ImageVector by lazy {
        icon(
            "Scan",
            "M3,11h8V3H3V11zM5,5h4v4H5V5zM3,21h8v-8H3V21zM5,15h4v4H5V15zM13,3v8h8V3H13zM19,9h-4V5h4V9zM19,19h2v2h-2z" +
                "M13,13h2v2h-2zM15,15h2v2h-2zM13,17h2v2h-2zM15,19h2v2h-2zM17,17h2v2h-2zM17,13h2v2h-2zM19,15h2v2h-2z",
        )
    }
    val Shield: ImageVector by lazy {
        icon("Shield", "M12,1L3,5v6c0,5.55 3.84,10.74 9,12 5.16,-1.26 9,-6.45 9,-12V5L12,1zM10,17l-4,-4 1.41,-1.41L10,14.17l6.59,-6.59L18,9l-8,8z")
    }
    val Wrench: ImageVector by lazy {
        icon(
            "Wrench",
            "M22.7,19l-9.1,-9.1c0.9,-2.3 0.4,-5 -1.5,-6.9 -2,-2 -5,-2.4 -7.4,-1.3L9,6 6,9 1.6,4.7C0.4,7.1 0.9,10.1 2.9,12.1" +
                "c1.9,1.9 4.6,2.4 6.9,1.5l9.1,9.1c0.4,0.4 1,0.4 1.4,0l2.3,-2.3c0.5,-0.4 0.5,-1.1 0.1,-1.4z",
        )
    }

    /** Every icon, for the test that parses their paths. */
    val all: List<ImageVector> get() = listOf(Chat, Agent, Devices, Computer, Phone, Code, Scan, Shield, Wrench)

    private fun icon(name: String, path: String): ImageVector =
        ImageVector.Builder(name = name, defaultWidth = 24.dp, defaultHeight = 24.dp, viewportWidth = 24f, viewportHeight = 24f)
            .addPath(pathData = addPathNodes(path), fill = SolidColor(Color.Black))
            .build()
}
